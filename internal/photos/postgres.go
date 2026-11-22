package photos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PostgresRepository stores photos in PostgreSQL using a shared pool.
// The caller owns the pool and is responsible for closing it.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository constructs a repository without opening a new pool.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

var _ Repository = (*PostgresRepository)(nil)

const photoColumns = `
	p.id, p.trip_id, p.batch_id, p.client_file_id, p.state, p.error_code,
	p.object_name, p.content_type, p.size_bytes, p.uploaded_at, p.processing_started_at,
	p.capture_time_local, p.capture_utc_offset, p.exif_lat, p.exif_lng, p.lat, p.lng,
	p.display_object_name, p.created_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPhoto(row rowScanner) (Photo, error) {
	var photo Photo
	var captureTime *time.Time
	err := row.Scan(
		&photo.ID, &photo.TripID, &photo.BatchID, &photo.ClientFileID, &photo.State, &photo.ErrorCode,
		&photo.ObjectName, &photo.ContentType, &photo.SizeBytes, &photo.UploadedAt, &photo.ProcessingStarted,
		&captureTime, &photo.CaptureUTCOffset, &photo.ExifLat, &photo.ExifLng, &photo.Lat, &photo.Lng,
		&photo.DisplayObjectName, &photo.CreatedAt,
	)
	if err != nil {
		return Photo{}, err
	}
	if captureTime != nil {
		local := captureTime.Format(CaptureTimeLayout)
		photo.CaptureTimeLocal = &local
	}
	return photo, nil
}

func (r *PostgresRepository) GetByClientFile(ctx context.Context, ownerID, batchID uuid.UUID, clientFileID string) (Photo, error) {
	photo, err := scanPhoto(r.db.QueryRowContext(ctx, `
		SELECT `+photoColumns+`
		FROM photos p JOIN trips t ON t.id = p.trip_id
		WHERE p.batch_id = $1 AND p.client_file_id = $2 AND t.owner_id = $3
	`, batchID, clientFileID, ownerID))
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, ErrNotFound
	}
	if err != nil {
		return Photo{}, fmt.Errorf("get photo: %w", err)
	}
	return photo, nil
}

func (r *PostgresRepository) Claim(ctx context.Context, photoID uuid.UUID, staleBefore time.Time) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE photos SET state = 'processing', processing_started_at = now()
		WHERE id = $1 AND (state = 'pending' OR (state = 'processing' AND processing_started_at < $2))
	`, photoID, staleBefore)
	if err != nil {
		return false, fmt.Errorf("claim photo: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim photo: %w", err)
	}
	return claimed == 1, nil
}

func (r *PostgresRepository) Release(ctx context.Context, photoID uuid.UUID) error {
	if _, err := r.db.ExecContext(ctx, `
		UPDATE photos SET state = 'pending', processing_started_at = NULL
		WHERE id = $1 AND state = 'processing'
	`, photoID); err != nil {
		return fmt.Errorf("release photo: %w", err)
	}
	return nil
}

func (r *PostgresRepository) Fail(ctx context.Context, photoID uuid.UUID, uploadedAt time.Time, code string) (Photo, error) {
	photo, err := scanPhoto(r.db.QueryRowContext(ctx, `
		UPDATE photos p SET state = 'failed', error_code = $2, uploaded_at = $3, processing_started_at = NULL
		WHERE p.id = $1 AND p.state = 'processing'
		RETURNING `+photoColumns,
		photoID, code, uploadedAt))
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, ErrNotFound
	}
	if err != nil {
		return Photo{}, fmt.Errorf("record failed photo: %w", err)
	}
	return photo, nil
}

func (r *PostgresRepository) Finish(ctx context.Context, photoID uuid.UUID, result Processed) (Photo, error) {
	state := StateNeedsLocation
	if result.ExifLat != nil && result.ExifLng != nil {
		state = StateReady
	}
	// The effective location starts as the EXIF location; owners correct it later.
	photo, err := scanPhoto(r.db.QueryRowContext(ctx, `
		UPDATE photos p SET
			state = $2, uploaded_at = $3, processing_started_at = NULL,
			capture_time_local = $4::timestamp, capture_utc_offset = $5,
			exif_lat = $6, exif_lng = $7, lat = $6, lng = $7,
			display_object_name = $8
		WHERE p.id = $1 AND p.state = 'processing'
		RETURNING `+photoColumns,
		photoID, state, result.UploadedAt, result.CaptureTimeLocal, result.CaptureUTCOffset,
		result.ExifLat, result.ExifLng, result.DisplayObjectName))
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, ErrNotFound
	}
	if err != nil {
		return Photo{}, fmt.Errorf("record processed photo: %w", err)
	}
	return photo, nil
}

func (r *PostgresRepository) Get(ctx context.Context, ownerID, photoID uuid.UUID) (Photo, error) {
	photo, err := scanPhoto(r.db.QueryRowContext(ctx, `
		SELECT `+photoColumns+`
		FROM photos p JOIN trips t ON t.id = p.trip_id
		WHERE p.id = $1 AND t.owner_id = $2
	`, photoID, ownerID))
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, ErrNotFound
	}
	if err != nil {
		return Photo{}, fmt.Errorf("get photo: %w", err)
	}
	return photo, nil
}

func (r *PostgresRepository) GetBatch(ctx context.Context, ownerID, batchID uuid.UUID) (uuid.UUID, []Photo, error) {
	var tripID uuid.UUID
	err := r.db.QueryRowContext(ctx, `
		SELECT b.trip_id FROM upload_batches b JOIN trips t ON t.id = b.trip_id
		WHERE b.id = $1 AND t.owner_id = $2
	`, batchID, ownerID).Scan(&tripID)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("get batch: %w", err)
	}
	photos, err := r.queryPhotos(ctx, `
		SELECT `+photoColumns+` FROM photos p
		WHERE p.batch_id = $1
		ORDER BY p.created_at, p.client_file_id
	`, batchID)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("list batch photos: %w", err)
	}
	return tripID, photos, nil
}

func (r *PostgresRepository) ListByTrip(ctx context.Context, ownerID, tripID uuid.UUID, after *Cursor, limit int) ([]Photo, error) {
	var owned bool
	if err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM trips WHERE id = $1 AND owner_id = $2)`, tripID, ownerID,
	).Scan(&owned); err != nil {
		return nil, fmt.Errorf("check trip owner: %w", err)
	}
	if !owned {
		return nil, ErrNotFound
	}

	var afterCapture *string
	var afterUpload *time.Time
	var afterID *uuid.UUID
	if after != nil {
		afterCapture, afterUpload, afterID = after.CaptureTimeLocal, after.UploadedAt, &after.ID
	}
	// Keyset pagination over the photos_trip_capture_idx expressions. infinity
	// puts missing values last and keeps the row comparison NULL-free.
	photos, err := r.queryPhotos(ctx, `
		SELECT `+photoColumns+` FROM photos p
		WHERE p.trip_id = $1
		  AND ($2::uuid IS NULL
		       OR (COALESCE(p.capture_time_local, 'infinity'), COALESCE(p.uploaded_at, 'infinity'), p.id)
		        > (COALESCE($3::timestamp, 'infinity'), COALESCE($4::timestamptz, 'infinity'), $2))
		ORDER BY COALESCE(p.capture_time_local, 'infinity'), COALESCE(p.uploaded_at, 'infinity'), p.id
		LIMIT $5
	`, tripID, afterID, afterCapture, afterUpload, limit)
	if err != nil {
		return nil, fmt.Errorf("list trip photos: %w", err)
	}
	return photos, nil
}

func (r *PostgresRepository) queryPhotos(ctx context.Context, query string, args ...any) ([]Photo, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	photos := make([]Photo, 0)
	for rows.Next() {
		photo, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		photos = append(photos, photo)
	}
	return photos, rows.Err()
}
