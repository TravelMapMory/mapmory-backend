//go:build integration

package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/TravelMapMory/mapmory-backend/internal/postgres"
)

const testTimeout = 10 * time.Second

// openTestDatabase connects only to the database provided by the
// isolated integration-test harness.
//
// Never fall back to DATABASE_URL: these tests write and delete rows.
func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("MAPMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("MAPMORY_TEST_DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		testTimeout,
	)
	defer cancel()

	db, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})

	return db
}

// testSubject generates a distinct Firebase-like UID for each test.
// These are synthetic identifiers, not real Firebase accounts.
func testSubject() string {
	return "test-" + uuid.NewString()
}

// cleanupSubject removes test data without depending on the test's
// original context, which might already have been canceled.
//
// Cleanup is registered after opening the pool, so Go executes it
// before the pool's cleanup callback.
func cleanupSubject(t *testing.T, db *sql.DB, subject string) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			testTimeout,
		)
		defer cancel()

		_, err := db.ExecContext(
			ctx,
			"DELETE FROM users WHERE auth_subject = $1",
			subject,
		)
		if err != nil {
			t.Errorf("remove test user: %v", err)
		}
	})
}

func TestFindOrCreateByAuthSubjectIntegration(t *testing.T) {
	db := openTestDatabase(t)
	repo := NewRepository(db)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		testTimeout,
	)
	defer cancel()

	subject := testSubject()
	cleanupSubject(t, db, subject)

	// First resolution should insert a new application user.
	firstID, err := repo.FindOrCreateByAuthSubject(ctx, subject)
	if err != nil {
		t.Fatalf("first resolution: %v", err)
	}
	if firstID == uuid.Nil {
		t.Fatal("first resolution returned a zero UUID")
	}

	// Repeated resolution should return the existing user.
	secondID, err := repo.FindOrCreateByAuthSubject(ctx, subject)
	if err != nil {
		t.Fatalf("second resolution: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("second ID = %s, want %s", secondID, firstID)
	}

	// Check the actual database state, not just return values.
	var count int
	err = db.QueryRowContext(
		ctx,
		"SELECT count(*) FROM users WHERE auth_subject = $1",
		subject,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count matching users: %v", err)
	}
	if count != 1 {
		t.Fatalf("matching users = %d, want 1", count)
	}
}

func TestFindOrCreateDistinctSubjects(t *testing.T) {
	db := openTestDatabase(t)
	repo := NewRepository(db)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		testTimeout,
	)
	defer cancel()

	firstSubject := testSubject()
	secondSubject := testSubject()

	cleanupSubject(t, db, firstSubject)
	cleanupSubject(t, db, secondSubject)

	firstID, err := repo.FindOrCreateByAuthSubject(ctx, firstSubject)
	if err != nil {
		t.Fatalf("resolve first user: %v", err)
	}

	secondID, err := repo.FindOrCreateByAuthSubject(ctx, secondSubject)
	if err != nil {
		t.Fatalf("resolve second user: %v", err)
	}

	if firstID == uuid.Nil || secondID == uuid.Nil {
		t.Fatal("resolution returned a zero UUID")
	}
	if firstID == secondID {
		t.Fatalf("different subjects resolved to the same ID: %s", firstID)
	}
}

func TestFindOrCreatePreservesExistingUser(t *testing.T) {
	db := openTestDatabase(t)
	repo := NewRepository(db)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		testTimeout,
	)
	defer cancel()

	subject := testSubject()
	cleanupSubject(t, db, subject)

	// Insert an existing user with profile data that provisioning
	// must not overwrite.
	var wantID uuid.UUID
	var wantCreatedAt time.Time

	err := db.QueryRowContext(
		ctx,
		`INSERT INTO users (auth_subject, display_name)
		 VALUES ($1, $2)
		 RETURNING id, created_at`,
		subject,
		"Existing User",
	).Scan(&wantID, &wantCreatedAt)
	if err != nil {
		t.Fatalf("insert existing user: %v", err)
	}

	gotID, err := repo.FindOrCreateByAuthSubject(ctx, subject)
	if err != nil {
		t.Fatalf("resolve existing user: %v", err)
	}
	if gotID != wantID {
		t.Fatalf("resolved ID = %s, want %s", gotID, wantID)
	}

	var displayName string
	var createdAt time.Time

	err = db.QueryRowContext(
		ctx,
		`SELECT display_name, created_at
		 FROM users
		 WHERE id = $1`,
		wantID,
	).Scan(&displayName, &createdAt)
	if err != nil {
		t.Fatalf("read existing user: %v", err)
	}

	if displayName != "Existing User" {
		t.Errorf("display name changed to %q", displayName)
	}
	if !createdAt.Equal(wantCreatedAt) {
		t.Errorf("creation timestamp changed")
	}
}

func TestFindOrCreateConcurrent(t *testing.T) {
	db := openTestDatabase(t)
	repo := NewRepository(db)

	subject := testSubject()
	cleanupSubject(t, db, subject)

	const callers = 24

	// Release all callers together to increase the likelihood that
	// their first INSERT attempts overlap.
	start := make(chan struct{})

	type result struct {
		id  uuid.UUID
		err error
	}

	results := make(chan result, callers)
	var wg sync.WaitGroup

	for i := 0; i < callers; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			ctx, cancel := context.WithTimeout(
				context.Background(),
				15*time.Second,
			)
			defer cancel()

			id, err := repo.FindOrCreateByAuthSubject(ctx, subject)
			results <- result{id: id, err: err}
		}()
	}

	close(start)
	wg.Wait()
	close(results)

	// Test assertions belong in the main test goroutine.
	// Avoid calling t.Fatal inside worker goroutines.
	var expectedID uuid.UUID

	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent resolution failed: %v", result.err)
		}
		if result.id == uuid.Nil {
			t.Fatal("concurrent resolution returned a zero UUID")
		}

		if expectedID == uuid.Nil {
			expectedID = result.id
		} else if result.id != expectedID {
			t.Fatalf(
				"concurrent IDs differ: got %s, want %s",
				result.id,
				expectedID,
			)
		}
	}

	// Verify the uniqueness guarantee at the database level.
	ctx, cancel := context.WithTimeout(
		context.Background(),
		testTimeout,
	)
	defer cancel()

	var count int
	err := db.QueryRowContext(
		ctx,
		"SELECT count(*) FROM users WHERE auth_subject = $1",
		subject,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count concurrent users: %v", err)
	}
	if count != 1 {
		t.Fatalf("concurrent calls created %d users, want 1", count)
	}
}

func TestFindOrCreateRejectsInvalidSubjects(t *testing.T) {
	db := openTestDatabase(t)
	repo := NewRepository(db)

	for _, subject := range []string{"", " ", "\t\n"} {
		t.Run(fmt.Sprintf("%q", subject), func(t *testing.T) {
			id, err := repo.FindOrCreateByAuthSubject(
				context.Background(),
				subject,
			)
			if err == nil {
				t.Fatal("expected invalid subject error")
			}
			if id != uuid.Nil {
				t.Fatalf("invalid subject returned ID %s", id)
			}
			if !strings.Contains(err.Error(), "auth subject is required") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFindOrCreateCanceledContext(t *testing.T) {
	db := openTestDatabase(t)
	repo := NewRepository(db)

	subject := testSubject()
	cleanupSubject(t, db, subject)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	id, err := repo.FindOrCreateByAuthSubject(ctx, subject)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if id != uuid.Nil {
		t.Fatalf("canceled request returned ID %s", id)
	}

	// Verify cancellation did not create a user.
	var count int
	err = db.QueryRowContext(
		context.Background(),
		"SELECT count(*) FROM users WHERE auth_subject = $1",
		subject,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count users after cancellation: %v", err)
	}
	if count != 0 {
		t.Fatalf("canceled request created %d users", count)
	}
}

func TestFindOrCreateDatabaseFailure(t *testing.T) {
	db := openTestDatabase(t)
	repo := NewRepository(db)

	if err := db.Close(); err != nil {
		t.Fatalf("close database pool: %v", err)
	}

	id, err := repo.FindOrCreateByAuthSubject(
		context.Background(),
		testSubject(),
	)

	if err == nil {
		t.Fatal("expected database error")
	}
	if id != uuid.Nil {
		t.Fatalf("database failure returned ID %s", id)
	}
}
