//go:build integration

package erasure

import (
	"context"
	"database/sql"
	"errors"
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
