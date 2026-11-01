package uploads

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// PostgresRepository stores batches in PostgreSQL using a shared pool.
// The caller owns the pool and is responsible for closing it.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository constructs a repository without opening a new pool.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

var _ Repository = (*PostgresRepository)(nil)

// CreateBatch inserts the batch only when the trip belongs to ownerID, so the
// ownership check and the insert cannot race.
func (r *PostgresRepository) CreateBatch(ctx context.Context, ownerID, tripID, batchID uuid.UUID, files []File) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin batch transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO upload_batches (id, trip_id)
		SELECT $1, id FROM trips WHERE id = $2 AND owner_id = $3
	`, batchID, tripID, ownerID)
	if err != nil {
		return fmt.Errorf("insert upload batch: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("insert upload batch: %w", err)
	}
	if inserted == 0 {
		return ErrTripNotFound
	}

	for _, file := range files {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, file.PhotoID, tripID, batchID, file.ClientFileID, file.ObjectName, file.ContentType, file.SizeBytes); err != nil {
			return fmt.Errorf("insert photo %s: %w", file.ClientFileID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upload batch: %w", err)
	}
	return nil
}
