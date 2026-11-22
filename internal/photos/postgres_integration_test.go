//go:build integration

package photos

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

const integrationTimeout = 10 * time.Second

// These tests create and delete database rows. Never fall back to DATABASE_URL.
func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MAPMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("MAPMORY_TEST_DATABASE_URL is required; run scripts/test-postgres-integration.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)
	return ctx
}

type fixture struct {
	db                     *sql.DB
	owner, tripID, batchID uuid.UUID
}

// newFixture inserts a user, a trip and a batch, removed when the test ends.
func newFixture(t *testing.T, db *sql.DB) fixture {
	t.Helper()
	ctx := testContext(t)
	f := fixture{db: db, batchID: uuid.New()}
	if err := db.QueryRowContext(ctx, "INSERT INTO users (auth_subject) VALUES ($1) RETURNING id",
		"photos-test-"+uuid.NewString()).Scan(&f.owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := db.QueryRowContext(ctx, "INSERT INTO trips (owner_id, title) VALUES ($1, 'Photos') RETURNING id",
		f.owner).Scan(&f.tripID); err != nil {
		t.Fatalf("insert trip: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO upload_batches (id, trip_id) VALUES ($1, $2)", f.batchID, f.tripID); err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()
		for _, statement := range []string{
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

// addPhoto inserts a pending photo, optionally already uploaded at uploadedAt.
func (f fixture) addPhoto(t *testing.T, clientFileID string, uploadedAt *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := f.db.ExecContext(testContext(t), `
		INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes, uploaded_at)
		VALUES ($1, $2, $3, $4, $5, 'image/jpeg', 10, $6)
	`, id, f.tripID, f.batchID, clientFileID, "originals/"+f.tripID.String()+"/"+id.String(), uploadedAt); err != nil {
		t.Fatalf("insert photo: %v", err)
	}
	return id
}

func TestLifecycleIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)
	id := f.addPhoto(t, "a", nil)

	photo, err := repo.GetByClientFile(ctx, f.owner, f.batchID, "a")
	if err != nil || photo.ID != id || photo.State != StatePending {
		t.Fatalf("get: %+v, %v", photo, err)
	}
	if _, err := repo.GetByClientFile(ctx, uuid.New(), f.batchID, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner: err=%v", err)
	}

	if claimed, err := repo.Claim(ctx, id, time.Now().Add(-time.Hour)); !claimed || err != nil {
		t.Fatalf("first claim: %v, %v", claimed, err)
	}
	if claimed, _ := repo.Claim(ctx, id, time.Now().Add(-time.Hour)); claimed {
		t.Fatal("a fresh claim must not be taken twice")
	}
	if claimed, _ := repo.Claim(ctx, id, time.Now().Add(time.Hour)); !claimed {
		t.Fatal("a stale claim must be reclaimable")
	}
	if err := repo.Release(ctx, id); err != nil {
		t.Fatal(err)
	}
	if photo, _ := repo.GetByClientFile(ctx, f.owner, f.batchID, "a"); photo.State != StatePending || photo.ProcessingStarted != nil {
		t.Fatalf("after release: %+v", photo)
	}

	if _, err := repo.Finish(ctx, id, Processed{UploadedAt: time.Now(), DisplayObjectName: "display/x.jpg"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finish without a claim: err=%v", err)
	}
	repo.Claim(ctx, id, time.Now())
	local, offset, lat, lng := "2025-07-14T09:30:00", "+03:00", 60.1868333, 24.8278
	uploaded := time.Date(2026, 11, 1, 10, 0, 0, 123456000, time.UTC)
	photo, err = repo.Finish(ctx, id, Processed{
		UploadedAt: uploaded, CaptureTimeLocal: &local, CaptureUTCOffset: &offset,
		ExifLat: &lat, ExifLng: &lng, DisplayObjectName: "display/" + id.String() + ".jpg",
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if photo.State != StateReady || *photo.CaptureTimeLocal != local || *photo.CaptureUTCOffset != offset ||
		*photo.ExifLat != lat || *photo.Lat != lat || *photo.Lng != lng || !photo.UploadedAt.Equal(uploaded) ||
		photo.ErrorCode != nil || photo.ProcessingStarted != nil {
		t.Fatalf("finished photo: %+v", photo)
	}
	if claimed, _ := repo.Claim(ctx, id, time.Now().Add(time.Hour)); claimed {
		t.Fatal("a processed photo must never be claimed again")
	}
}

func TestFailAndNeedsLocationIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)

	bad := f.addPhoto(t, "bad", nil)
	repo.Claim(ctx, bad, time.Now())
	photo, err := repo.Fail(ctx, bad, time.Now(), "invalid_image")
	if err != nil || photo.State != StateFailed || *photo.ErrorCode != "invalid_image" || photo.DisplayObjectName != nil {
		t.Fatalf("fail: %+v, %v", photo, err)
	}

	plain := f.addPhoto(t, "plain", nil)
	repo.Claim(ctx, plain, time.Now())
	photo, err = repo.Finish(ctx, plain, Processed{UploadedAt: time.Now(), DisplayObjectName: "display/" + plain.String() + ".jpg"})
	if err != nil || photo.State != StateNeedsLocation || photo.Lat != nil || photo.CaptureTimeLocal != nil {
		t.Fatalf("needs-location: %+v, %v", photo, err)
	}
}

func TestGetBatchIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	f.addPhoto(t, "a", nil)
	f.addPhoto(t, "b", nil)

	tripID, photos, err := repo.GetBatch(testContext(t), f.owner, f.batchID)
	if err != nil || tripID != f.tripID || len(photos) != 2 {
		t.Fatalf("batch: trip=%s photos=%d err=%v", tripID, len(photos), err)
	}
	if _, _, err := repo.GetBatch(testContext(t), uuid.New(), f.batchID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner: err=%v", err)
	}
}

// setCaptureTime stores a camera-local capture time on a photo
func (f fixture) setCaptureTime(t *testing.T, id uuid.UUID, local string) {
	t.Helper()
	if _, err := f.db.ExecContext(testContext(t),
		"UPDATE photos SET capture_time_local = $2::timestamp WHERE id = $1", id, local); err != nil {
		t.Fatalf("set capture time: %v", err)
	}
}

// byID orders two IDs the way PostgreSQL orders UUIDs
func byID(a, b uuid.UUID) (uuid.UUID, uuid.UUID) {
	if a.String() > b.String() {
		return b, a
	}
	return a, b
}

func TestListByTripPaginationIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	at := func(minutes int) *time.Time {
		value := base.Add(time.Duration(minutes) * time.Minute)
		return &value
	}

	// capture time decides, even against upload order
	late := f.addPhoto(t, "late", at(0))
	f.setCaptureTime(t, late, "2026-07-02T08:00:00")
	early := f.addPhoto(t, "early", at(9))
	f.setCaptureTime(t, early, "2026-07-01T08:00:00")
	// equal capture times: the earlier upload first, then the lower ID
	tieUpload := f.addPhoto(t, "tie-upload", at(5))
	f.setCaptureTime(t, tieUpload, "2026-07-01T12:00:00")
	tieA, tieB := f.addPhoto(t, "tie-a", at(1)), f.addPhoto(t, "tie-b", at(1))
	f.setCaptureTime(t, tieA, "2026-07-01T12:00:00")
	f.setCaptureTime(t, tieB, "2026-07-01T12:00:00")
	tieA, tieB = byID(tieA, tieB)
	// undated photos come last, then photos that were never uploaded
	undated := f.addPhoto(t, "undated", at(2))
	pendingA, pendingB := byID(f.addPhoto(t, "p1", nil), f.addPhoto(t, "p2", nil))
	want := []uuid.UUID{early, tieA, tieB, tieUpload, late, undated, pendingA, pendingB}

	var got []uuid.UUID
	var cursor *Cursor
	for pages := 0; pages < 10; pages++ {
		page, err := repo.ListByTrip(testContext(t), f.owner, f.tripID, cursor, 2)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, photo := range page {
			got = append(got, photo.ID)
		}
		if len(page) < 2 {
			break
		}
		// Round-trip the cursor through its opaque encoding, as clients do.
		next, err := DecodeCursor(CursorAfter(page[len(page)-1]).Encode())
		if err != nil {
			t.Fatal(err)
		}
		cursor = &next
	}
	if len(got) != len(want) {
		t.Fatalf("got %d photos, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: got %s want %s\n got %v\nwant %v", i, got[i], want[i], got, want)
		}
	}

	if _, err := repo.ListByTrip(testContext(t), uuid.New(), f.tripID, nil, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner: err=%v", err)
	}
}

// getPhoto returns one of the caller's photos (contract getPhoto)
func TestGetIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	id := f.addPhoto(t, "a", nil)
	f.setCaptureTime(t, id, "2026-07-01T12:00:00")
	photo, err := repo.Get(testContext(t), f.owner, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if photo.ID != id || photo.TripID != f.tripID || photo.CaptureTimeLocal == nil || *photo.CaptureTimeLocal != "2026-07-01T12:00:00" {
		t.Fatalf("photo = %+v", photo)
	}
	if _, err := repo.Get(testContext(t), uuid.New(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner: err=%v", err)
	}
	if _, err := repo.Get(testContext(t), f.owner, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown photo: err=%v", err)
	}
}

// processedPhoto inserts a photo already in state with an optional position.
func (f fixture) processedPhoto(t *testing.T, state string, lat, lng *float64) uuid.UUID {
	t.Helper()
	id := f.addPhoto(t, uuid.NewString(), nil)
	if _, err := f.db.ExecContext(testContext(t), `
		UPDATE photos SET state = $2, uploaded_at = now(), display_object_name = $3,
			exif_lat = $4, exif_lng = $5, lat = $4, lng = $5
		WHERE id = $1
	`, id, state, "display/"+id.String()+".jpg", lat, lng); err != nil {
		t.Fatalf("mark processed: %v", err)
	}
	return id
}

func TestPlaceLabelsIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)
	lat, lng := 34.967, 135.773
	id := f.processedPhoto(t, StateReady, &lat, &lng)
	kyoto := Labels{Country: "Japan", City: "Kyoto", PlaceName: "Fushimi Inari Taisha", Provider: "geoapify", Attribution: "Powered by Geoapify"}

	photo, err := repo.Get(ctx, f.owner, id)
	if err != nil || photo.LabelStatus != LabelPending || photo.City != nil {
		t.Fatalf("new photo labels: %+v, %v", photo, err)
	}
	if _, err := repo.ConfirmLabels(ctx, f.owner, id); !errors.Is(err, ErrNoSuggestion) {
		t.Fatalf("confirm without suggestion: err=%v", err)
	}

	// A suggestion for a stale position is ignored.
	if stored, err := repo.SuggestLabels(ctx, id, 1, 2, kyoto); stored || err != nil {
		t.Fatalf("stale suggestion: %v, %v", stored, err)
	}
	if stored, err := repo.SuggestLabels(ctx, id, lat, lng, kyoto); !stored || err != nil {
		t.Fatalf("suggest: %v, %v", stored, err)
	}
	photo, _ = repo.Get(ctx, f.owner, id)
	if photo.LabelStatus != LabelSuggested || *photo.City != "Kyoto" || *photo.LabelProvider != "geoapify" {
		t.Fatalf("suggested: %+v", photo)
	}

	if _, err := repo.ConfirmLabels(ctx, uuid.New(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger confirm: err=%v", err)
	}
	photo, err = repo.ConfirmLabels(ctx, f.owner, id)
	if err != nil || photo.LabelStatus != LabelConfirmed {
		t.Fatalf("confirm: %+v, %v", photo, err)
	}
	if stored, _ := repo.SuggestLabels(ctx, id, lat, lng, Labels{City: "Osaka"}); stored {
		t.Fatal("a suggestion overwrote confirmed labels")
	}
}

func TestRelocateIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)
	exifLat, exifLng := 60.18, 24.93
	located := f.processedPhoto(t, StateReady, &exifLat, &exifLng)
	unlocated := f.processedPhoto(t, StateNeedsLocation, nil, nil)
	pending := f.addPhoto(t, "pending", nil)

	photo, err := repo.Relocate(ctx, f.owner, unlocated, 35.0, 135.7, &Labels{City: "Kyoto", Country: "Japan", Provider: "geoapify"})
	if err != nil || photo.State != StateReady || *photo.Lat != 35.0 || photo.ExifLat != nil ||
		photo.LabelStatus != LabelConfirmed || *photo.City != "Kyoto" || photo.PlaceName != nil {
		t.Fatalf("relocate unlocated: %+v, %v", photo, err)
	}

	// Without labels the position still saves; labels are cleared and pending.
	photo, err = repo.Relocate(ctx, f.owner, located, 1.5, 2.5, nil)
	if err != nil || *photo.Lat != 1.5 || *photo.ExifLat != exifLat || *photo.ExifLng != exifLng ||
		photo.LabelStatus != LabelPending || photo.City != nil {
		t.Fatalf("relocate located: %+v, %v", photo, err)
	}

	if _, err := repo.Relocate(ctx, f.owner, pending, 1, 2, nil); !errors.Is(err, ErrNotProcessed) {
		t.Fatalf("pending photo: err=%v", err)
	}
	if _, err := repo.Relocate(ctx, uuid.New(), located, 1, 2, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger: err=%v", err)
	}
	if _, err := repo.Relocate(ctx, f.owner, uuid.New(), 1, 2, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing photo: err=%v", err)
	}
	if photo, _ := repo.Get(ctx, f.owner, located); *photo.Lat != 1.5 {
		t.Fatalf("a stranger's request changed the photo: %+v", photo)
	}
}
