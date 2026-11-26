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
	p.display_object_name, p.created_at,
	p.country, p.city, p.place_name, p.label_status, p.label_provider, p.label_attribution`

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
		&photo.Country, &photo.City, &photo.PlaceName, &photo.LabelStatus, &photo.LabelProvider, &photo.LabelAttribution,
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

// nullIfEmpty stores an empty label as NULL, which the API reports as null.
func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (r *PostgresRepository) SuggestLabels(ctx context.Context, photoID uuid.UUID, lat, lng float64, labels Labels) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE photos SET country = $4, city = $5, place_name = $6,
			label_status = 'suggested', label_provider = $7, label_attribution = $8
		WHERE id = $1 AND lat = $2 AND lng = $3 AND label_status = 'pending'
	`, photoID, lat, lng, nullIfEmpty(labels.Country), nullIfEmpty(labels.City), nullIfEmpty(labels.PlaceName),
		nullIfEmpty(labels.Provider), nullIfEmpty(labels.Attribution))
	if err != nil {
		return false, fmt.Errorf("suggest photo labels: %w", err)
	}
	stored, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("suggest photo labels: %w", err)
	}
	return stored == 1, nil
}

func (r *PostgresRepository) Relocate(ctx context.Context, ownerID, photoID uuid.UUID, lat, lng float64, labels *Labels) (Photo, error) {
	status := LabelPending
	var set Labels
	if labels != nil {
		status, set = LabelConfirmed, *labels
	}
	photo, err := scanPhoto(r.db.QueryRowContext(ctx, `
		UPDATE photos p SET state = 'ready', lat = $3, lng = $4,
			country = $5, city = $6, place_name = $7, label_status = $8,
			label_provider = $9, label_attribution = $10
		FROM trips t
		WHERE p.id = $1 AND t.id = p.trip_id AND t.owner_id = $2
		  AND p.state IN ('ready', 'needs-location')
		RETURNING `+photoColumns,
		photoID, ownerID, lat, lng, nullIfEmpty(set.Country), nullIfEmpty(set.City), nullIfEmpty(set.PlaceName),
		status, nullIfEmpty(set.Provider), nullIfEmpty(set.Attribution)))
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, r.explainMiss(ctx, ownerID, photoID, ErrNotProcessed)
	}
	if err != nil {
		return Photo{}, fmt.Errorf("relocate photo: %w", err)
	}
	return photo, nil
}

func (r *PostgresRepository) ConfirmLabels(ctx context.Context, ownerID, photoID uuid.UUID) (Photo, error) {
	photo, err := scanPhoto(r.db.QueryRowContext(ctx, `
		UPDATE photos p SET label_status = 'confirmed'
		FROM trips t
		WHERE p.id = $1 AND t.id = p.trip_id AND t.owner_id = $2 AND p.label_status = 'suggested'
		RETURNING `+photoColumns,
		photoID, ownerID))
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, r.explainMiss(ctx, ownerID, photoID, ErrNoSuggestion)
	}
	if err != nil {
		return Photo{}, fmt.Errorf("confirm photo labels: %w", err)
	}
	return photo, nil
}

// explainMiss tells an absent or foreign photo (ErrNotFound) apart from one
// the caller owns but whose current state refused the update (conflict).
func (r *PostgresRepository) explainMiss(ctx context.Context, ownerID, photoID uuid.UUID, conflict error) error {
	if _, err := r.Get(ctx, ownerID, photoID); err != nil {
		return err
	}
	return conflict
}

func (r *PostgresRepository) ClaimLabelRetries(ctx context.Context, ownerID, tripID uuid.UUID, limit int) ([]Photo, error) {
	// SKIP LOCKED lets two simultaneous listings retry different photos.
	photos, err := r.queryPhotos(ctx, `
		UPDATE photos p SET label_attempts = p.label_attempts + 1
		WHERE p.id IN (
			SELECT q.id FROM photos q JOIN trips t ON t.id = q.trip_id
			WHERE q.trip_id = $1 AND t.owner_id = $2 AND q.state IN ('ready', 'needs-location')
			  AND q.lat IS NOT NULL AND q.label_status = 'pending' AND q.label_attempts < $3
			ORDER BY q.id
			LIMIT $4
			FOR UPDATE OF q SKIP LOCKED
		)
		RETURNING `+photoColumns,
		tripID, ownerID, MaxLabelRetries, limit)
	if err != nil {
		return nil, fmt.Errorf("claim label retries: %w", err)
	}
	return photos, nil
}
