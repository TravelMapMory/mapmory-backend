//go:build integration

package erasure

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
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

func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(testContext(t), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(testContext(t), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// newUser inserts a user, removed (if still present) when the test ends.
func newUser(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(testContext(t), "INSERT INTO users (auth_subject) VALUES ($1) RETURNING id",
		"erasure-test-"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = NewPostgresRepository(db).DeleteAccount(ctx, id)
	})
	return id
}

// trip inserts a trip with one batch, a processed photo, a pending photo and
// a link share of the processed photo: 6 rows across the five tables. It returns the trip and its objects.
func trip(t *testing.T, db *sql.DB, owner uuid.UUID) (uuid.UUID, []string) {
	t.Helper()
	var tripID uuid.UUID
	if err := db.QueryRowContext(testContext(t), "INSERT INTO trips (owner_id, title) VALUES ($1, 'Trip') RETURNING id",
		owner).Scan(&tripID); err != nil {
		t.Fatalf("insert trip: %v", err)
	}
	batch, processed, pending, share := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(t, db, "INSERT INTO upload_batches (id, trip_id) VALUES ($1, $2)", batch, tripID)
	original := "originals/" + tripID.String() + "/" + processed.String()
	display := "display/" + tripID.String() + "/" + processed.String() + ".jpg"
	pendingOriginal := "originals/" + tripID.String() + "/" + pending.String()
	exec(t, db, `INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes,
		state, display_object_name, uploaded_at) VALUES ($1, $2, $3, 'a', $4, 'image/jpeg', 10, 'needs-location', $5, now())`,
		processed, tripID, batch, original, display)
	exec(t, db, `INSERT INTO photos (id, trip_id, batch_id, client_file_id, object_name, content_type, size_bytes)
		VALUES ($1, $2, $3, 'b', $4, 'image/jpeg', 10)`, pending, tripID, batch, pendingOriginal)
	exec(t, db, `INSERT INTO shares (id, trip_id, mode, title, token) VALUES ($1, $2, 'link', 'S', $3)`,
		share, tripID, "token-"+uuid.NewString())
	exec(t, db, `INSERT INTO share_photos (share_id, photo_id, trip_id) VALUES ($1, $2, $3)`, share, processed, tripID)
	return tripID, []string{original, display, pendingOriginal}
}

func rowsOf(t *testing.T, db *sql.DB, tripID uuid.UUID) int {
	t.Helper()
	return count(t, db, `SELECT
		(SELECT count(*) FROM trips WHERE id = $1) + (SELECT count(*) FROM photos WHERE trip_id = $1) +
		(SELECT count(*) FROM upload_batches WHERE trip_id = $1) + (SELECT count(*) FROM shares WHERE trip_id = $1) +
		(SELECT count(*) FROM share_photos WHERE trip_id = $1)`, tripID)
}

func sorted(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func TestDeleteTripIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)
	owner, stranger := newUser(t, db), newUser(t, db)
	doomed, objects := trip(t, db, owner)
	kept, _ := trip(t, db, owner)

	if _, err := repo.DeleteTrip(ctx, stranger, doomed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger delete: err=%v", err)
	}
	if rowsOf(t, db, doomed) == 0 {
		t.Fatal("a stranger's request deleted rows")
	}

	got, err := repo.DeleteTrip(ctx, owner, doomed)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if want := sorted(objects); len(got) != len(want) || sorted(got)[0] != want[0] || sorted(got)[2] != want[2] {
		t.Fatalf("objects = %v, want %v", got, want)
	}
	if n := rowsOf(t, db, doomed); n != 0 {
		t.Fatalf("%d rows of the deleted trip remain", n)
	}
	if rowsOf(t, db, kept) != 6 {
		t.Fatal("deleting one trip touched another")
	}
	if _, err := repo.DeleteTrip(ctx, owner, doomed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: err=%v", err)
	}
}

func TestDeleteAccountIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)
	owner, other := newUser(t, db), newUser(t, db)
	first, firstObjects := trip(t, db, owner)
	second, secondObjects := trip(t, db, owner)
	theirs, _ := trip(t, db, other)

	got, err := repo.DeleteAccount(ctx, owner)
	if err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if len(got) != len(firstObjects)+len(secondObjects) {
		t.Fatalf("objects = %d, want %d", len(got), len(firstObjects)+len(secondObjects))
	}
	if rowsOf(t, db, first)+rowsOf(t, db, second) != 0 || count(t, db, "SELECT count(*) FROM users WHERE id = $1", owner) != 0 {
		t.Fatal("the account's rows remain")
	}
	if rowsOf(t, db, theirs) != 6 || count(t, db, "SELECT count(*) FROM users WHERE id = $1", other) != 1 {
		t.Fatal("deleting an account touched another user")
	}

	// An account without trips (or already deleted) deletes cleanly.
	if objects, err := repo.DeleteAccount(ctx, owner); err != nil || len(objects) != 0 {
		t.Fatalf("repeat: %v, %v", objects, err)
	}
}

func photoIDOf(t *testing.T, db *sql.DB, tripID uuid.UUID, clientFileID string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(testContext(t), "SELECT id FROM photos WHERE trip_id = $1 AND client_file_id = $2",
		tripID, clientFileID).Scan(&id); err != nil {
		t.Fatalf("photo %s: %v", clientFileID, err)
	}
	return id
}

// recorder is a RemoveObjects that records its objects, or fails with err.
type recorder struct {
	got [][]string
	err error
}

func (r *recorder) remove(_ context.Context, objects []string) error {
	r.got = append(r.got, append([]string(nil), objects...))
	return r.err
}

func TestDeletePhotoIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)
	owner, stranger := newUser(t, db), newUser(t, db)
	tripID, objects := trip(t, db, owner)
	kept, _ := trip(t, db, owner)
	processed, pending := photoIDOf(t, db, tripID, "a"), photoIDOf(t, db, tripID, "b")
	now := time.Now()
	quiet := PhotoQuiet{UploadsBefore: now.Add(-20 * time.Minute), ProcessingBefore: now.Add(-10 * time.Minute)}

	// Another owner's photo and a missing photo are the same not-found.
	for _, c := range []struct {
		owner, photo uuid.UUID
	}{{stranger, processed}, {owner, uuid.New()}} {
		rec := &recorder{}
		if err := repo.DeletePhoto(ctx, c.owner, c.photo, quiet, rec.remove); !errors.Is(err, ErrPhotoNotFound) || len(rec.got) != 0 {
			t.Fatalf("owner %s photo %s: err=%v removed=%v", c.owner, c.photo, err, rec.got)
		}
	}

	// A photo whose upload URL is still valid is busy, and nothing is removed.
	rec := &recorder{}
	if err := repo.DeletePhoto(ctx, owner, pending, quiet, rec.remove); !errors.Is(err, ErrPhotoBusy) || len(rec.got) != 0 {
		t.Fatalf("pending: err=%v removed=%v", err, rec.got)
	}

	// Storage failure: the photo and its share selection stay for a retry.
	failing := &recorder{err: fmt.Errorf("bucket unavailable")}
	if err := repo.DeletePhoto(ctx, owner, processed, quiet, failing.remove); !errors.Is(err, ErrCleanup) {
		t.Fatalf("storage failure: err=%v", err)
	}
	if n := rowsOf(t, db, tripID); n != 6 {
		t.Fatalf("after a storage failure %d rows remain, want 6", n)
	}

	// Success: original and display image are removed, then the row; the
	// share stays but no longer selects the photo; everything else is intact.
	rec = &recorder{}
	if err := repo.DeletePhoto(ctx, owner, processed, quiet, rec.remove); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(rec.got) != 1 || len(rec.got[0]) != 2 || rec.got[0][0] != objects[0] || rec.got[0][1] != objects[1] {
		t.Fatalf("removed %v, want [%s %s]", rec.got, objects[0], objects[1])
	}
	if n := count(t, db, "SELECT count(*) FROM photos WHERE id = $1", processed); n != 0 {
		t.Fatal("the photo row remains")
	}
	if n := count(t, db, "SELECT count(*) FROM share_photos WHERE photo_id = $1", processed); n != 0 {
		t.Fatal("a share still selects the deleted photo")
	}
	if count(t, db, "SELECT count(*) FROM trips WHERE id = $1", tripID) != 1 ||
		count(t, db, "SELECT count(*) FROM photos WHERE id = $1", pending) != 1 ||
		count(t, db, "SELECT count(*) FROM shares WHERE trip_id = $1", tripID) != 1 ||
		count(t, db, "SELECT count(*) FROM upload_batches WHERE trip_id = $1", tripID) != 1 {
		t.Fatal("deleting one photo removed its trip, another photo, a share or the batch")
	}
	if rowsOf(t, db, kept) != 6 {
		t.Fatal("deleting a photo touched another trip")
	}
	if err := repo.DeletePhoto(ctx, owner, processed, quiet, rec.remove); !errors.Is(err, ErrPhotoNotFound) {
		t.Fatalf("repeat: err=%v", err)
	}

	// Once its upload URL has expired, a pending photo can go too; it has no
	// display image.
	expired := PhotoQuiet{UploadsBefore: now.Add(time.Hour), ProcessingBefore: quiet.ProcessingBefore}
	rec = &recorder{}
	if err := repo.DeletePhoto(ctx, owner, pending, expired, rec.remove); err != nil {
		t.Fatalf("expired pending: %v", err)
	}
	if len(rec.got) != 1 || len(rec.got[0]) != 1 || rec.got[0][0] != objects[2] {
		t.Fatalf("removed %v, want [%s]", rec.got, objects[2])
	}
}
