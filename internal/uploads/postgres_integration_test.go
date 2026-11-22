//go:build integration

package uploads

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
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close integration database: %v", err)
		}
	})
	return db
}

func integrationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)
	return ctx
}

// createOwnerWithTrip inserts a user and one trip, and removes them together
// with any batches and photos when the test ends.
func createOwnerWithTrip(t *testing.T, db *sql.DB) (ownerID, tripID uuid.UUID) {
	t.Helper()
	ctx := integrationContext(t)
	if err := db.QueryRowContext(ctx,
		"INSERT INTO users (auth_subject) VALUES ($1) RETURNING id", "upload-test-"+uuid.NewString(),
	).Scan(&ownerID); err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		"INSERT INTO trips (owner_id, title) VALUES ($1, 'Upload test') RETURNING id", ownerID,
	).Scan(&tripID); err != nil {
		t.Fatalf("insert test trip: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()
		// Foreign keys use ON DELETE RESTRICT: remove children first.
		for _, statement := range []string{
			"DELETE FROM photos WHERE trip_id = $1",
			"DELETE FROM upload_batches WHERE trip_id = $1",
			"DELETE FROM trips WHERE id = $1",
		} {
			if _, err := db.ExecContext(cleanupCtx, statement, tripID); err != nil {
				t.Errorf("clean up: %s: %v", statement, err)
			}
		}
		if _, err := db.ExecContext(cleanupCtx, "DELETE FROM users WHERE id = $1", ownerID); err != nil {
			t.Errorf("clean up test user: %v", err)
		}
	})
	return ownerID, tripID
}

func testFile(tripID uuid.UUID, clientFileID string) File {
	photoID := uuid.New()
	return File{
		PhotoID:      photoID,
		ClientFileID: clientFileID,
		ContentType:  "image/jpeg",
		SizeBytes:    1234,
		ObjectName:   ObjectName(tripID, photoID),
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(integrationContext(t), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func TestCreateBatchStoresPendingPhotosIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner, trip := createOwnerWithTrip(t, db)
	repo := NewPostgresRepository(db)
	batchID := uuid.New()
	files := []File{testFile(trip, "a"), testFile(trip, "b")}

	if err := repo.CreateBatch(integrationContext(t), owner, trip, batchID, files); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if n := countRows(t, db, "SELECT count(*) FROM upload_batches WHERE id = $1 AND trip_id = $2", batchID, trip); n != 1 {
		t.Fatalf("batch rows = %d", n)
	}
	for _, file := range files {
		var state, object, contentType string
		var size int64
		var gotTrip, gotBatch uuid.UUID
		if err := db.QueryRowContext(integrationContext(t), `
			SELECT trip_id, batch_id, state, object_name, content_type, size_bytes
			FROM photos WHERE id = $1 AND client_file_id = $2
		`, file.PhotoID, file.ClientFileID).Scan(&gotTrip, &gotBatch, &state, &object, &contentType, &size); err != nil {
			t.Fatalf("read photo %s: %v", file.ClientFileID, err)
		}
		if gotTrip != trip || gotBatch != batchID || state != "pending" || object != file.ObjectName ||
			contentType != file.ContentType || size != file.SizeBytes {
			t.Fatalf("photo %s stored as trip=%s batch=%s state=%s object=%s type=%s size=%d",
				file.ClientFileID, gotTrip, gotBatch, state, object, contentType, size)
		}
	}
}

func TestCreateBatchEmptyIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner, trip := createOwnerWithTrip(t, db)
	batchID := uuid.New()
	if err := NewPostgresRepository(db).CreateBatch(integrationContext(t), owner, trip, batchID, nil); err != nil {
		t.Fatalf("create empty batch: %v", err)
	}
	if n := countRows(t, db, "SELECT count(*) FROM upload_batches WHERE id = $1", batchID); n != 1 {
		t.Fatalf("batch rows = %d", n)
	}
}

func TestCreateBatchOwnerIsolationIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	_, aliceTrip := createOwnerWithTrip(t, db)
	bob, _ := createOwnerWithTrip(t, db)
	batchID := uuid.New()

	err := NewPostgresRepository(db).CreateBatch(integrationContext(t), bob, aliceTrip, batchID, []File{testFile(aliceTrip, "a")})
	if !errors.Is(err, ErrTripNotFound) {
		t.Fatalf("err = %v, want ErrTripNotFound", err)
	}
	if n := countRows(t, db, "SELECT count(*) FROM upload_batches WHERE id = $1", batchID); n != 0 {
		t.Fatalf("batch rows = %d", n)
	}
	if n := countRows(t, db, "SELECT count(*) FROM photos WHERE trip_id = $1", aliceTrip); n != 0 {
		t.Fatalf("photo rows = %d", n)
	}

	err = NewPostgresRepository(db).CreateBatch(integrationContext(t), bob, uuid.New(), batchID, nil)
	if !errors.Is(err, ErrTripNotFound) {
		t.Fatalf("missing trip: err = %v, want ErrTripNotFound", err)
	}
}

func TestCreateBatchIsAtomicIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner, trip := createOwnerWithTrip(t, db)
	batchID := uuid.New()

	// The second file violates photos_batch_client_file_unique.
	err := NewPostgresRepository(db).CreateBatch(integrationContext(t), owner, trip, batchID,
		[]File{testFile(trip, "same"), testFile(trip, "same")})
	if err == nil {
		t.Fatal("expected duplicate client_file_id to fail")
	}
	if n := countRows(t, db, "SELECT count(*) FROM upload_batches WHERE id = $1", batchID); n != 0 {
		t.Fatalf("batch rows after rollback = %d", n)
	}
	if n := countRows(t, db, "SELECT count(*) FROM photos WHERE batch_id = $1", batchID); n != 0 {
		t.Fatalf("photo rows after rollback = %d", n)
	}
}
