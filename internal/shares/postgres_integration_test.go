//go:build integration

package shares

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/TravelMapMory/mapmory-backend/internal/postgres"
)

// These tests create and delete database rows. Never fall back to DATABASE_URL.
func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MAPMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("MAPMORY_TEST_DATABASE_URL is required; run scripts/test-postgres-integration.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type tripFixture struct {
	db                     *sql.DB
	owner, tripID, batchID uuid.UUID
}

// newTrip inserts a user with one trip and batch, removed when the test ends.
func newTrip(t *testing.T, db *sql.DB) tripFixture {
	t.Helper()
	ctx := testContext(t)
	f := tripFixture{db: db, batchID: uuid.New()}
	if err := db.QueryRowContext(ctx, "INSERT INTO users (auth_subject) VALUES ($1) RETURNING id",
		"shares-test-"+uuid.NewString()).Scan(&f.owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := db.QueryRowContext(ctx, "INSERT INTO trips (owner_id, title) VALUES ($1, 'Shared trip') RETURNING id",
		f.owner).Scan(&f.tripID); err != nil {
		t.Fatalf("insert trip: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO upload_batches (id, trip_id) VALUES ($1, $2)", f.batchID, f.tripID); err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, statement := range []string{
			"DELETE FROM shares WHERE trip_id = $1",
			"DELETE FROM photos WHERE trip_id = $1",
			"DELETE FROM upload_batches WHERE trip_id = $1",
			"DELETE FROM trips WHERE id = $1",
		} {
			if _, err := db.ExecContext(ctx, statement, f.tripID); err != nil {
				t.Errorf("clean up: %v", err)
			}
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", f.owner); err != nil {
			t.Errorf("clean up user: %v", err)
		}
	})
	return f
}

// photo inserts a photo in state; a processed one gets a display image, an
// exact position and labels.
func (f tripFixture) photo(t *testing.T, state, capture string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	processed := state == "ready" || state == "needs-location"
	var display, captureArg, errorCode any
	if processed {
		display = "display/" + id.String() + ".jpg"
	}
	if state == "failed" {
		errorCode = "invalid_image"
	}
	if capture != "" {
		captureArg = capture
	}
	if _, err := f.db.ExecContext(testContext(t), `
		INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes,
			state, display_object_name, capture_time_local, uploaded_at, lat, lng, exif_lat, exif_lng, city, country, error_code)
		VALUES ($1, $2, $3, $4, $5, 'image/jpeg', 10, $6, $7, $8::timestamp, now(),
			35.0123, 135.7765, 35.0123, 135.7765, 'Kyoto', 'Japan', $9)
	`, id, f.tripID, f.batchID, id.String(), "originals/"+id.String(), state, display, captureArg, errorCode); err != nil {
		t.Fatalf("insert photo: %v", err)
	}
	return id
}

func selected(ids ...uuid.UUID) []Selected {
	lat, lng := 35.01, 135.77
	result := make([]Selected, len(ids))
	for i, id := range ids {
		result[i] = Selected{PhotoID: id, DisplayLat: &lat, DisplayLng: &lng}
	}
	return result
}

func TestShareLifecycleIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newTrip(t, db)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)
	later := f.photo(t, "ready", "2025-05-14T10:00:00")
	earlier := f.photo(t, "ready", "2025-05-12T09:00:00")
	private := f.photo(t, "ready", "2025-05-13T09:00:00")
	pending := f.photo(t, "pending", "")

	candidates, err := repo.Candidates(ctx, f.owner, f.tripID, []uuid.UUID{later, earlier, pending})
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates = %+v, %v; a pending photo is not shareable", candidates, err)
	}
	if _, err := repo.Candidates(ctx, uuid.New(), f.tripID, []uuid.UUID{later}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger candidates: err=%v", err)
	}

	description := "Our best days"
	share, err := repo.Create(ctx, f.owner, NewShare{TripID: f.tripID, Mode: ModeLink, Title: "Kyoto", Description: &description,
		Photos: selected(later, earlier)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(share.Token) != 22 || share.CreatedAt.IsZero() || len(share.PhotoIDs) != 2 {
		t.Fatalf("share = %+v", share)
	}

	list, err := repo.ListByTrip(ctx, f.owner, f.tripID)
	if err != nil || len(list) != 1 || list[0].Token != share.Token || len(list[0].PhotoIDs) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := repo.ListByTrip(ctx, uuid.New(), f.tripID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger list: err=%v", err)
	}

	// The recipient sees exactly the selected photos, in capture order, at the
	// stored coarse position and never the private one.
	c, err := repo.ByToken(ctx, share.Token)
	if err != nil {
		t.Fatalf("by token: %v", err)
	}
	if c.Title != "Kyoto" || *c.Description != description || len(c.Photos) != 2 ||
		c.Photos[0].ID != earlier || c.Photos[1].ID != later {
		t.Fatalf("collection = %+v", c)
	}
	for _, p := range c.Photos {
		if p.ID == private || *p.DisplayLat != 35.01 || *p.City != "Kyoto" || p.CaptureTimeLocal == nil {
			t.Fatalf("shared photo = %+v", p)
		}
	}
	if _, err := repo.ByToken(ctx, "unknown-token-unknown-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: err=%v", err)
	}
	// A link share is not public.
	if _, err := repo.PublicByID(ctx, share.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("link share as public: err=%v", err)
	}

	if err := repo.Revoke(ctx, uuid.New(), share.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger revoke: err=%v", err)
	}
	if err := repo.Revoke(ctx, f.owner, share.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := repo.Revoke(ctx, f.owner, share.ID); err != nil {
		t.Fatalf("revoke twice: %v", err)
	}
	if _, err := repo.ByToken(ctx, share.Token); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked link: err=%v", err)
	}
	if list, _ := repo.ListByTrip(ctx, f.owner, f.tripID); len(list) != 0 {
		t.Fatalf("revoked share still listed: %+v", list)
	}
}

func TestShareRejectsForeignAndUnprocessedPhotosIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newTrip(t, db)
	other := newTrip(t, db)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)
	mine := f.photo(t, "ready", "")
	theirs := other.photo(t, "ready", "")
	failed := f.photo(t, "failed", "")

	for name, ids := range map[string][]uuid.UUID{"other trip": {mine, theirs}, "unprocessed": {mine, failed}} {
		if _, err := repo.Create(ctx, f.owner, NewShare{TripID: f.tripID, Mode: ModeLink, Title: "x", Photos: selected(ids...)}); !errors.Is(err, ErrInvalidPhotos) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
	if _, err := repo.Create(ctx, other.owner, NewShare{TripID: f.tripID, Mode: ModeLink, Title: "x", Photos: selected(mine)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("share of a stranger's trip: err=%v", err)
	}
	// Nothing was stored by the refused attempts.
	if list, _ := repo.ListByTrip(ctx, f.owner, f.tripID); len(list) != 0 {
		t.Fatalf("refused shares stored: %+v", list)
	}
	// The database itself refuses a photo of another trip.
	share, err := repo.Create(ctx, f.owner, NewShare{TripID: f.tripID, Mode: ModeLink, Title: "ok", Photos: selected(mine)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO share_photos (share_id, photo_id, trip_id) VALUES ($1, $2, $3)`,
		share.ID, theirs, f.tripID); err == nil {
		t.Fatal("share_photos accepted a photo of another trip")
	}
}

func TestPublicSharesIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newTrip(t, db)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)
	a := f.photo(t, "ready", "")
	b := f.photo(t, "ready", "")

	public, err := repo.Create(ctx, f.owner, NewShare{TripID: f.tripID, Mode: ModePublic, Title: "Public Kyoto", Photos: selected(a)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, f.owner, NewShare{TripID: f.tripID, Mode: ModeLink, Title: "Link only", Photos: selected(b)}); err != nil {
		t.Fatal(err)
	}

	points, err := repo.PublicPoints(ctx, 135, 34, 136, 36, 100)
	if err != nil {
		t.Fatal(err)
	}
	mine := 0
	for _, p := range points {
		if p.ShareID == public.ID {
			mine++
			if p.PhotoID != a || p.Lat != 35.01 {
				t.Fatalf("public point = %+v; want the coarse position", p)
			}
		}
		if p.PhotoID == b {
			t.Fatal("a link-only photo reached the public map")
		}
	}
	if mine != 1 {
		t.Fatalf("public points of this share = %d", mine)
	}
	if outside, _ := repo.PublicPoints(ctx, 0, 0, 10, 10, 100); containsShare(outside, public.ID) {
		t.Fatal("a point outside the box was returned")
	}
	// West > east crosses the antimeridian: 170..-170 does not contain Kyoto.
	if across, _ := repo.PublicPoints(ctx, 170, -90, -170, 90, 100); containsShare(across, public.ID) {
		t.Fatal("antimeridian box returned Kyoto")
	}
	if c, err := repo.PublicByID(ctx, public.ID); err != nil || len(c.Photos) != 1 {
		t.Fatalf("public share = %+v, %v", c, err)
	}

	if err := repo.Revoke(ctx, f.owner, public.ID); err != nil {
		t.Fatal(err)
	}
	if after, _ := repo.PublicPoints(ctx, 135, 34, 136, 36, 100); containsShare(after, public.ID) {
		t.Fatal("an unpublished share stayed on the public map")
	}
	if _, err := repo.PublicByID(ctx, public.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpublished share: err=%v", err)
	}
}

func containsShare(points []PublicPoint, id uuid.UUID) bool {
	for _, p := range points {
		if p.ShareID == id {
			return true
		}
	}
	return false
}
