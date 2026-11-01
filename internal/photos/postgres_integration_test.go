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

func TestListByTripPaginationIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	// Two photos share an upload time to exercise the ID tie-breaker.
	var want []uuid.UUID
	times := []time.Time{base, base.Add(time.Minute), base.Add(time.Minute), base.Add(2 * time.Minute)}
	ids := make([]uuid.UUID, len(times))
	for i, at := range times {
		at := at
		ids[i] = f.addPhoto(t, string(rune('a'+i)), &at)
	}
	tied := []uuid.UUID{ids[1], ids[2]}
	if tied[0].String() < tied[1].String() {
		tied[0], tied[1] = tied[1], tied[0]
	}
	want = append(want, ids[3], tied[0], tied[1], ids[0])
	pendingA, pendingB := f.addPhoto(t, "p1", nil), f.addPhoto(t, "p2", nil)
	if pendingA.String() < pendingB.String() {
		pendingA, pendingB = pendingB, pendingA
	}
	want = append(want, pendingA, pendingB)

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
