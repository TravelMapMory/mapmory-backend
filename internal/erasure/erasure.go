// Package erasure deletes an owner's content (design doc 2.2 use case 6, 3.4
// and 3.6): one photo, one trip with its photos, batches and shares, or a whole
// account. Database rows go in one transaction. For a trip or an account the
// caller then removes the returned storage objects, because object storage is
// not transactional; a single photo's objects are removed first (DeletePhoto).
package erasure

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned for a trip that is absent or not the caller's.
var ErrNotFound = errors.New("trip not found")

// ErrPhotoNotFound is returned for a photo that is absent or not the caller's.
// The two cases are deliberately indistinguishable.
var ErrPhotoNotFound = errors.New("photo not found")

// ErrPhotoBusy is returned for a photo whose upload URL is still valid or that
// is being processed: deleting it then could leave an object behind that is
// written after the deletion. The caller can try again a little later.
var ErrPhotoBusy = errors.New("photo is still uploading or processing")

// ErrCleanup wraps a storage failure while removing a photo's objects. The
// photo row is kept, but objects removed before the failure stay removed: the
// deletion is retryable, not atomic. Repeating it completes it, because
// removing a missing object succeeds.
var ErrCleanup = errors.New("photo objects could not be removed")

// Photo states DeletePhoto distinguishes (see the photos package).
const (
	statePending    = "pending"
	stateProcessing = "processing"
)

// PhotoQuiet says when a photo that is not yet processed is safe to delete.
type PhotoQuiet struct {
	// UploadsBefore: a pending photo registered before this time can no longer
	// receive its upload, because its signed upload URL has expired.
	UploadsBefore time.Time
	// ProcessingBefore: processing that started before this time is stale and
	// may be reclaimed (the same rule completeUpload uses).
	ProcessingBefore time.Time
}

// RemoveObjects deletes stored objects. A missing object is not an error, so
// repeating a removal is safe.
type RemoveObjects func(ctx context.Context, objectNames []string) error

// Repository deletes content. ownerID must be a trusted, server-resolved
// application-user ID.
type Repository interface {
	// DeleteTrip removes the caller's trip, its photos, upload batches and
	// shares, and returns the storage objects (originals and display images)
	// to remove. Links to its shares stop working at once.
	DeleteTrip(ctx context.Context, ownerID, tripID uuid.UUID) ([]string, error)
	// DeleteAccount removes every trip of the caller as DeleteTrip does, then
	// the application-user record, and returns the storage objects to remove.
	// The Firebase identity is not touched; the client deletes it.
	DeleteAccount(ctx context.Context, ownerID uuid.UUID) ([]string, error)
	// DeletePhoto removes one of the caller's photos and nothing else; the
	// trip, its other photos and its shares stay. The photo row is locked,
	// remove deletes its original and display image, and only then is the row
	// deleted. This is retryable, not atomic: after ErrCleanup the row remains
	// but some objects may already be gone, and a repeated call finishes the
	// job. The lock keeps other requests from changing the row meanwhile; it
	// cannot stop stale processing from writing a display object to storage,
	// which that processing removes itself once it finds the row gone. Share
	// selections drop the photo by ON DELETE CASCADE; trip covers, counts and
	// map results are derived from the remaining photos.
	// It returns ErrPhotoNotFound, ErrPhotoBusy or ErrCleanup.
	DeletePhoto(ctx context.Context, ownerID, photoID uuid.UUID, quiet PhotoQuiet, remove RemoveObjects) error
}

// PostgresRepository implements Repository on a shared pool it does not own.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository constructs a repository without opening a new pool.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

var _ Repository = (*PostgresRepository)(nil)

func (r *PostgresRepository) DeleteTrip(ctx context.Context, ownerID, tripID uuid.UUID) ([]string, error) {
	return r.inTx(ctx, func(tx *sql.Tx) ([]string, error) {
		// Locking the trip makes a concurrent upload registration wait, so no
		// photo row can be added between collecting objects and deleting.
		var locked uuid.UUID
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM trips WHERE id = $1 AND owner_id = $2 FOR UPDATE`, tripID, ownerID,
		).Scan(&locked)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("lock trip: %w", err)
		}
		return deleteTrips(ctx, tx, `id = $1`, tripID)
	})
}

func (r *PostgresRepository) DeleteAccount(ctx context.Context, ownerID uuid.UUID) ([]string, error) {
	return r.inTx(ctx, func(tx *sql.Tx) ([]string, error) {
		if _, err := tx.ExecContext(ctx, `SELECT id FROM trips WHERE owner_id = $1 FOR UPDATE`, ownerID); err != nil {
			return nil, fmt.Errorf("lock trips: %w", err)
		}
		objects, err := deleteTrips(ctx, tx, `owner_id = $1`, ownerID)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, ownerID); err != nil {
			return nil, fmt.Errorf("delete user: %w", err)
		}
		return objects, nil
	})
}

func (r *PostgresRepository) DeletePhoto(ctx context.Context, ownerID, photoID uuid.UUID, quiet PhotoQuiet, remove RemoveObjects) error {
	_, err := r.inTx(ctx, func(tx *sql.Tx) ([]string, error) {
		// FOR UPDATE makes a concurrent claim, processing result or location
		// change wait; once the row is gone they find nothing to update.
		var (
			state             string
			createdAt         time.Time
			processingStarted *time.Time
			original          string
			display           *string
		)
		err := tx.QueryRowContext(ctx, `
			SELECT p.state, p.created_at, p.processing_started_at, p.object_name, p.display_object_name
			FROM photos p JOIN trips t ON t.id = p.trip_id
			WHERE p.id = $1 AND t.owner_id = $2
			FOR UPDATE OF p
		`, photoID, ownerID).Scan(&state, &createdAt, &processingStarted, &original, &display)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPhotoNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("lock photo: %w", err)
		}
		if photoBusy(state, createdAt, processingStarted, quiet) {
			return nil, ErrPhotoBusy
		}
		objects := []string{original}
		if display != nil {
			objects = append(objects, *display)
		}
		if err := remove(ctx, objects); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrCleanup, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM photos WHERE id = $1`, photoID); err != nil {
			return nil, fmt.Errorf("delete photo: %w", err)
		}
		return nil, nil
	})
	return err
}

// photoBusy reports whether an object could still be written for the photo:
// its upload URL is valid, or another request is processing it.
func photoBusy(state string, createdAt time.Time, processingStarted *time.Time, quiet PhotoQuiet) bool {
	switch state {
	case statePending:
		return !createdAt.Before(quiet.UploadsBefore)
	case stateProcessing:
		return processingStarted == nil || !processingStarted.Before(quiet.ProcessingBefore)
	default:
		return false
	}
}

func (r *PostgresRepository) inTx(ctx context.Context, work func(*sql.Tx) ([]string, error)) ([]string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	objects, err := work(tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit deletion: %w", err)
	}
	return objects, nil
}

// deleteTrips removes the trips matching where (with $1 = arg) and everything
// that references them, in foreign-key order. Shares and share_photos go by
// ON DELETE CASCADE.
func deleteTrips(ctx context.Context, tx *sql.Tx, where string, arg any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT p.object_name, p.display_object_name
		FROM photos p WHERE p.trip_id IN (SELECT id FROM trips WHERE `+where+`)
	`, arg)
	if err != nil {
		return nil, fmt.Errorf("collect photo objects: %w", err)
	}
	objects := make([]string, 0)
	for rows.Next() {
		var original string
		var display *string
		if err := rows.Scan(&original, &display); err != nil {
			rows.Close()
			return nil, fmt.Errorf("collect photo objects: %w", err)
		}
		objects = append(objects, original)
		if display != nil {
			objects = append(objects, *display)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("collect photo objects: %w", err)
	}
	rows.Close()

	for _, statement := range []string{
		`DELETE FROM photos WHERE trip_id IN (SELECT id FROM trips WHERE ` + where + `)`,
		`DELETE FROM upload_batches WHERE trip_id IN (SELECT id FROM trips WHERE ` + where + `)`,
		`DELETE FROM trips WHERE ` + where,
	} {
		if _, err := tx.ExecContext(ctx, statement, arg); err != nil {
			return nil, fmt.Errorf("delete trip content: %w", err)
		}
	}
	return objects, nil
}
