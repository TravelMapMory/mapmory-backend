package shares

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PostgresRepository stores shares in PostgreSQL using a shared pool. The
// caller owns the pool and is responsible for closing it.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository constructs a repository without opening a new pool.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

var _ Repository = (*PostgresRepository)(nil)

func uuidArray(ids []uuid.UUID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String()
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (r *PostgresRepository) ownsTrip(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ownerID, tripID uuid.UUID) error {
	var owned bool
	if err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM trips WHERE id = $1 AND owner_id = $2)`, tripID, ownerID,
	).Scan(&owned); err != nil {
		return fmt.Errorf("check trip owner: %w", err)
	}
	if !owned {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) Candidates(ctx context.Context, ownerID, tripID uuid.UUID, ids []uuid.UUID) ([]Candidate, error) {
	if err := r.ownsTrip(ctx, r.db, ownerID, tripID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, lat, lng, city, country FROM photos
		WHERE trip_id = $1 AND id = ANY($2::uuid[])
		  AND state IN ('ready', 'needs-location') AND display_object_name IS NOT NULL
	`, tripID, uuidArray(ids))
	if err != nil {
		return nil, fmt.Errorf("share candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]Candidate, 0, len(ids))
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ID, &c.Lat, &c.Lng, &c.City, &c.Country); err != nil {
			return nil, fmt.Errorf("share candidates: %w", err)
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

func (r *PostgresRepository) Create(ctx context.Context, ownerID uuid.UUID, input NewShare) (Share, error) {
	token, err := NewToken()
	if err != nil {
		return Share{}, fmt.Errorf("share token: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Share{}, fmt.Errorf("begin share: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := r.ownsTrip(ctx, tx, ownerID, input.TripID); err != nil {
		return Share{}, err
	}
	share := Share{
		ID: uuid.New(), TripID: input.TripID, Mode: input.Mode, Title: input.Title,
		Description: input.Description, Token: token, PhotoIDs: make([]uuid.UUID, 0, len(input.Photos)),
	}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO shares (id, trip_id, mode, title, description, token)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at
	`, share.ID, share.TripID, share.Mode, share.Title, share.Description, share.Token).Scan(&share.CreatedAt); err != nil {
		return Share{}, fmt.Errorf("insert share: %w", err)
	}
	for _, photo := range input.Photos {
		// The composite foreign key rejects a photo of another trip; only
		// processed photos are inserted, which the state check re-verifies.
		result, err := tx.ExecContext(ctx, `
			INSERT INTO share_photos (share_id, photo_id, trip_id, display_lat, display_lng)
			SELECT $1, p.id, p.trip_id, $4, $5 FROM photos p
			WHERE p.id = $2 AND p.trip_id = $3
			  AND p.state IN ('ready', 'needs-location') AND p.display_object_name IS NOT NULL
		`, share.ID, photo.PhotoID, share.TripID, photo.DisplayLat, photo.DisplayLng)
		if err != nil {
			return Share{}, fmt.Errorf("insert share photo: %w", err)
		}
		if inserted, err := result.RowsAffected(); err != nil || inserted != 1 {
			return Share{}, ErrInvalidPhotos
		}
		share.PhotoIDs = append(share.PhotoIDs, photo.PhotoID)
	}
	if err := tx.Commit(); err != nil {
		return Share{}, fmt.Errorf("commit share: %w", err)
	}
	return share, nil
}

// ListByTrip returns the owner's live shares of a trip, newest first, marking
// those hidden by an unresolved report so the owner can see why.
func (r *PostgresRepository) ListByTrip(ctx context.Context, ownerID, tripID uuid.UUID) ([]Share, error) {
	if err := r.ownsTrip(ctx, r.db, ownerID, tripID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.trip_id, s.mode, s.title, s.description, s.token, s.created_at, s.revoked_at,
			COALESCE(string_agg(sp.photo_id::text, ',' ORDER BY sp.photo_id), ''),
			NOT `+notUnderReview("s.id")+`
		FROM shares s LEFT JOIN share_photos sp ON sp.share_id = s.id
		WHERE s.trip_id = $1 AND s.revoked_at IS NULL
		GROUP BY s.id
		ORDER BY s.created_at DESC, s.id DESC
	`, tripID)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	defer rows.Close()
	result := make([]Share, 0)
	for rows.Next() {
		var s Share
		var ids string
		if err := rows.Scan(&s.ID, &s.TripID, &s.Mode, &s.Title, &s.Description, &s.Token, &s.CreatedAt, &s.RevokedAt, &ids, &s.UnderReview); err != nil {
			return nil, fmt.Errorf("list shares: %w", err)
		}
		s.PhotoIDs, err = parseUUIDList(ids)
		if err != nil {
			return nil, fmt.Errorf("list shares: %w", err)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// parseUUIDList reads a comma-separated list of UUIDs.
func parseUUIDList(text string) ([]uuid.UUID, error) {
	if text == "" {
		return []uuid.UUID{}, nil
	}
	parts := strings.Split(text, ",")
	ids := make([]uuid.UUID, len(parts))
	for i, part := range parts {
		id, err := uuid.Parse(part)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}

func (r *PostgresRepository) Revoke(ctx context.Context, ownerID, shareID uuid.UUID) error {
	var found bool
	err := r.db.QueryRowContext(ctx, `
		WITH target AS (
			SELECT s.id FROM shares s JOIN trips t ON t.id = s.trip_id
			WHERE s.id = $1 AND t.owner_id = $2
		), updated AS (
			UPDATE shares SET revoked_at = now()
			WHERE id IN (SELECT id FROM target) AND revoked_at IS NULL
		)
		SELECT EXISTS (SELECT 1 FROM target)
	`, shareID, ownerID).Scan(&found)
	if err != nil {
		return fmt.Errorf("revoke share: %w", err)
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

// collection reads a share header (by the given condition) and its photos.
func (r *PostgresRepository) collection(ctx context.Context, where string, arg any) (Collection, *time.Time, error) {
	var c Collection
	var revoked *time.Time
	err := r.db.QueryRowContext(ctx, `
		SELECT id, title, description, created_at, revoked_at FROM shares WHERE `+where, arg,
	).Scan(&c.ShareID, &c.Title, &c.Description, &c.CreatedAt, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Collection{}, nil, ErrNotFound
	}
	if err != nil {
		return Collection{}, nil, fmt.Errorf("get share: %w", err)
	}
	if revoked != nil {
		return c, revoked, nil
	}
	// A photo that is no longer processed (it cannot be, today) is skipped
	// rather than shown without an image.
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id, p.display_object_name, to_char(p.capture_time_local, 'YYYY-MM-DD"T"HH24:MI:SS'),
			p.city, p.country, sp.display_lat, sp.display_lng
		FROM share_photos sp JOIN photos p ON p.id = sp.photo_id
		WHERE sp.share_id = $1 AND p.state IN ('ready', 'needs-location') AND p.display_object_name IS NOT NULL
		ORDER BY COALESCE(p.capture_time_local, 'infinity'), COALESCE(p.uploaded_at, 'infinity'), p.id
	`, c.ShareID)
	if err != nil {
		return Collection{}, nil, fmt.Errorf("share photos: %w", err)
	}
	defer rows.Close()
	c.Photos = make([]SharedPhoto, 0)
	for rows.Next() {
		var p SharedPhoto
		if err := rows.Scan(&p.ID, &p.DisplayObjectName, &p.CaptureTimeLocal, &p.City, &p.Country, &p.DisplayLat, &p.DisplayLng); err != nil {
			return Collection{}, nil, fmt.Errorf("share photos: %w", err)
		}
		c.Photos = append(c.Photos, p)
	}
	return c, nil, rows.Err()
}

func (r *PostgresRepository) ByToken(ctx context.Context, token string) (Collection, error) {
	c, revoked, err := r.collection(ctx, "token = $1", token)
	if err != nil {
		return Collection{}, err
	}
	if revoked != nil {
		return Collection{}, ErrRevoked
	}
	return c, nil
}

func (r *PostgresRepository) PublicByID(ctx context.Context, shareID uuid.UUID) (Collection, error) {
	c, revoked, err := r.collection(ctx, "id = $1 AND mode = 'public' AND "+notUnderReview("shares.id"), shareID)
	if err != nil {
		return Collection{}, err
	}
	if revoked != nil {
		return Collection{}, ErrNotFound
	}
	return c, nil
}

func (r *PostgresRepository) PublicPoints(ctx context.Context, west, south, east, north float64, limit int) ([]PublicPoint, error) {
	lngCondition := "sp.display_lng BETWEEN $3 AND $4"
	if west > east {
		lngCondition = "(sp.display_lng >= $3 OR sp.display_lng <= $4)"
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id, s.id, s.title, p.display_object_name, sp.display_lat, sp.display_lng, p.city, p.country
		FROM shares s
		JOIN share_photos sp ON sp.share_id = s.id
		JOIN photos p ON p.id = sp.photo_id
		WHERE s.mode = 'public' AND s.revoked_at IS NULL AND `+notUnderReview("s.id")+`
		  AND p.state IN ('ready', 'needs-location') AND p.display_object_name IS NOT NULL
		  AND sp.display_lat BETWEEN $1 AND $2 AND `+lngCondition+`
		ORDER BY s.id, p.id
		LIMIT $5
	`, south, north, west, east, limit)
	if err != nil {
		return nil, fmt.Errorf("public points: %w", err)
	}
	defer rows.Close()
	points := make([]PublicPoint, 0)
	for rows.Next() {
		var p PublicPoint
		if err := rows.Scan(&p.PhotoID, &p.ShareID, &p.ShareTitle, &p.DisplayObjectName, &p.Lat, &p.Lng, &p.City, &p.Country); err != nil {
			return nil, fmt.Errorf("public points: %w", err)
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// notUnderReview is a condition that hides the public share in column while
// a report on it is unresolved.
func notUnderReview(column string) string {
	return `NOT EXISTS (SELECT 1 FROM share_reports sr WHERE sr.share_id = ` + column + ` AND sr.resolved_at IS NULL)`
}

func (r *PostgresRepository) Report(ctx context.Context, shareID uuid.UUID, reason string) error {
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO share_reports (id, share_id, reason)
		SELECT $1, s.id, $3 FROM shares s
		WHERE s.id = $2 AND s.mode = 'public' AND s.revoked_at IS NULL
	`, uuid.New(), shareID, reason)
	if err != nil {
		return fmt.Errorf("report share: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrNotFound
	}
	return nil
}
