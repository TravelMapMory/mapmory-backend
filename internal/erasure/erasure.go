// Package erasure deletes an owner's content (design doc 2.2 use case 6, 3.4
// and 3.6): one trip with its photos, batches and shares, or a whole account.
// Database rows go in one transaction; the caller then removes the returned
// storage objects, because object storage is not transactional.
package erasure

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrNotFound is returned for a trip that is absent or not the caller's.
var ErrNotFound = errors.New("trip not found")

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
