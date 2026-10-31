//go:build integration

package trips

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
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

// Create a real user to satisfy the trip owner foreign key. Register cleanup
// after opening the pool so all fixture rows are removed before pool closure.
func createIntegrationOwner(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	subject := "trip-test-" + uuid.NewString()
	var ownerID uuid.UUID
	if err := db.QueryRowContext(ctx,
		"INSERT INTO users (auth_subject) VALUES ($1) RETURNING id", subject,
	).Scan(&ownerID); err != nil {
		t.Fatalf("insert test user: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cleanupCancel()
		// trips.owner_id uses ON DELETE RESTRICT: remove trips first.
		if _, err := db.ExecContext(cleanupCtx, "DELETE FROM trips WHERE owner_id = $1", ownerID); err != nil {
			t.Errorf("clean up test trips: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, "DELETE FROM users WHERE id = $1", ownerID); err != nil {
			t.Errorf("clean up test user: %v", err)
		}
	})
	return ownerID
}

func integrationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)
	return ctx
}

func TestPostgresRepositoryCreateAndGetIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner := createIntegrationOwner(t, db)
	ctx := integrationContext(t)
	repo := NewPostgresRepository(db)

	created, err := repo.Create(ctx, owner, "Tokyo", "Autumn trip")
	if err != nil {
		t.Fatalf("create trip: %v", err)
	}
	if created.ID == uuid.Nil || created.OwnerID != owner || created.Title != "Tokyo" || created.Notes != "Autumn trip" {
		t.Fatalf("unexpected created trip: %+v", created)
	}
	if created.CreatedAt.IsZero() || created.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("invalid database timestamp: %v", created.CreatedAt)
	}

	got, err := repo.GetByID(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("get created trip: %v", err)
	}
	if got.ID != created.ID || got.OwnerID != created.OwnerID || got.Title != created.Title || got.Notes != created.Notes || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("retrieved trip %+v differs from created %+v", got, created)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM trips WHERE id = $1 AND owner_id = $2", created.ID, owner).Scan(&count); err != nil {
		t.Fatalf("check persisted row: %v", err)
	}
	if count != 1 {
		t.Fatalf("persisted row count = %d, want 1", count)
	}
}

func TestPostgresRepositoryOwnerIsolationIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	alice := createIntegrationOwner(t, db)
	bob := createIntegrationOwner(t, db)
	ctx := integrationContext(t)
	repo := NewPostgresRepository(db)

	aliceTrip, err := repo.Create(ctx, alice, "Alice trip", "")
	if err != nil {
		t.Fatalf("create Alice trip: %v", err)
	}
	bobTrip, err := repo.Create(ctx, bob, "Bob trip", "")
	if err != nil {
		t.Fatalf("create Bob trip: %v", err)
	}

	for _, tc := range []struct {
		name                      string
		owner, ownTrip, otherTrip uuid.UUID
	}{
		{"alice", alice, aliceTrip.ID, bobTrip.ID},
		{"bob", bob, bobTrip.ID, aliceTrip.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, err := repo.ListByOwner(ctx, tc.owner)
			if err != nil {
				t.Fatalf("list trips: %v", err)
			}
			if len(list) != 1 || list[0].ID != tc.ownTrip || list[0].OwnerID != tc.owner {
				t.Fatalf("owner-scoped list = %+v", list)
			}
			if _, err := repo.GetByID(ctx, tc.owner, tc.ownTrip); err != nil {
				t.Fatalf("get own trip: %v", err)
			}
			if _, err := repo.GetByID(ctx, tc.owner, tc.otherTrip); !errors.Is(err, ErrNotFound) {
				t.Fatalf("get another user's trip: got %v, want ErrNotFound", err)
			}
		})
	}
	if _, err := repo.GetByID(ctx, alice, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get unknown trip: got %v, want ErrNotFound", err)
	}
}

func TestPostgresRepositoryEmptyListIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner := createIntegrationOwner(t, db)
	list, err := NewPostgresRepository(db).ListByOwner(integrationContext(t), owner)
	if err != nil {
		t.Fatalf("list empty owner: %v", err)
	}
	if list == nil || len(list) != 0 {
		t.Fatalf("empty list = %#v, want non-nil empty slice", list)
	}
}

func TestPostgresRepositoryOrderingIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner := createIntegrationOwner(t, db)
	ctx := integrationContext(t)
	repo := NewPostgresRepository(db)

	var created []Trip
	for _, name := range []string{"first", "second", "third"} {
		trip, err := repo.Create(ctx, owner, name, "")
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		created = append(created, trip)
	}

	// Explicit test fixture timestamps make the ordering deterministic and
	// exercise the ID tie-breaker without relying on wall-clock sleeps.
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for i, trip := range created {
		stamp := base
		if i == 2 {
			stamp = base.Add(time.Hour)
		}
		if _, err := db.ExecContext(ctx, "UPDATE trips SET created_at = $1 WHERE id = $2", stamp, trip.ID); err != nil {
			t.Fatalf("set fixture timestamp: %v", err)
		}
	}
	list, err := repo.ListByOwner(ctx, owner)
	if err != nil {
		t.Fatalf("list ordered trips: %v", err)
	}
	if len(list) != 3 || list[0].ID != created[2].ID {
		t.Fatalf("newest trip should be first: %+v", list)
	}
	if list[1].CreatedAt != list[2].CreatedAt {
		t.Fatalf("fixture tied timestamps differ: %v, %v", list[1].CreatedAt, list[2].CreatedAt)
	}
	if strings.Compare(list[1].ID.String(), list[2].ID.String()) <= 0 {
		t.Fatalf("ID tie-breaker not descending: %s, %s", list[1].ID, list[2].ID)
	}
}

func TestPostgresRepositoryRejectsUnknownOwnerIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := integrationContext(t)
	repo := NewPostgresRepository(db)

	if _, err := repo.Create(ctx, uuid.New(), "orphan", ""); err == nil {
		t.Fatal("creating a trip with nonexistent owner succeeded")
	}
}

func TestPostgresRepositoryRejectsBlankTitlesIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner := createIntegrationOwner(t, db)
	ctx := integrationContext(t)
	repo := NewPostgresRepository(db)

	for _, title := range []string{
		"",
		"   ",
		"\t",
		"\n",
		"\r",
		"\f",
		"\v",
		"  \t  ",
		" \n\t ",
	} {
		t.Run(fmt.Sprintf("title=%q", title), func(t *testing.T) {
			if _, err := repo.Create(ctx, owner, title, ""); err == nil {
				t.Fatalf("creating trip with blank title %q succeeded", title)
			}
		})
	}

	list, err := repo.ListByOwner(ctx, owner)
	if err != nil {
		t.Fatalf("list trips after rejected inserts: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("rejected inserts left %d trips: %+v", len(list), list)
	}
}

func TestPostgresRepositoryPreservesValidTitleIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner := createIntegrationOwner(t, db)
	ctx := integrationContext(t)
	repo := NewPostgresRepository(db)

	const title = "  Japan 2026  "
	created, err := repo.Create(ctx, owner, title, "")
	if err != nil {
		t.Fatalf("create trip with surrounding whitespace: %v", err)
	}
	if created.Title != title {
		t.Fatalf("created title = %q, want %q", created.Title, title)
	}

	persisted, err := repo.GetByID(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("get persisted trip: %v", err)
	}
	if persisted.Title != title {
		t.Fatalf("persisted title = %q, want %q", persisted.Title, title)
	}
}

func TestPostgresRepositoryCancelledContextIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner := createIntegrationOwner(t, db)
	repo := NewPostgresRepository(db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := repo.Create(ctx, owner, "cancelled", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("create with cancelled context: %v", err)
	}
	if _, err := repo.ListByOwner(ctx, owner); !errors.Is(err, context.Canceled) {
		t.Fatalf("list with cancelled context: %v", err)
	}
	if _, err := repo.GetByID(ctx, owner, uuid.New()); !errors.Is(err, context.Canceled) {
		t.Fatalf("get with cancelled context: %v", err)
	}
}

func TestPostgresRepositoryClosedDatabaseIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner := createIntegrationOwner(t, db)
	// Cleanup of fixtures requires an open connection, so use a separate pool
	// for the deliberately broken repository.
	broken := openIntegrationDB(t)
	repo := NewPostgresRepository(broken)
	if err := broken.Close(); err != nil {
		t.Fatalf("close repository pool: %v", err)
	}
	ctx := integrationContext(t)
	if _, err := repo.Create(ctx, owner, "unavailable", ""); err == nil {
		t.Fatal("create with closed pool unexpectedly succeeded")
	}
	if _, err := repo.ListByOwner(ctx, owner); err == nil {
		t.Fatal("list with closed pool unexpectedly succeeded")
	}
	if _, err := repo.GetByID(ctx, owner, uuid.New()); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("get with closed pool: got %v, want database failure (not ErrNotFound)", err)
	}
}

func TestPostgresRepositoryPersistsAcrossPoolsIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner := createIntegrationOwner(t, db)
	ctx := integrationContext(t)
	created, err := NewPostgresRepository(db).Create(ctx, owner, "Persistent", "After restart")
	if err != nil {
		t.Fatalf("create trip: %v", err)
	}

	// A new pool and repository approximate application reinitialization.
	// The original pool stays alive for fixture cleanup.
	secondDB := openIntegrationDB(t)
	got, err := NewPostgresRepository(secondDB).GetByID(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("get trip through new pool: %v", err)
	}
	if got.ID != created.ID || got.Title != created.Title || got.Notes != created.Notes || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("trip changed across pools: got %+v, want %+v", got, created)
	}
}

func TestPostgresRepositoryUpdateIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	alice := createIntegrationOwner(t, db)
	bob := createIntegrationOwner(t, db)
	ctx := integrationContext(t)
	repo := NewPostgresRepository(db)
	created, err := repo.Create(ctx, alice, "Original", "Original notes")
	if err != nil {
		t.Fatal(err)
	}
	title := "Renamed"
	renamed, err := repo.Update(ctx, alice, created.ID, UpdateFields{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != title || renamed.Notes != created.Notes {
		t.Fatalf("title-only update: %+v", renamed)
	}
	notes := "Changed notes"
	changed, err := repo.Update(ctx, alice, created.ID, UpdateFields{Notes: &notes})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Title != title || changed.Notes != notes {
		t.Fatalf("notes-only update: %+v", changed)
	}
	empty := ""
	cleared, err := repo.Update(ctx, alice, created.ID, UpdateFields{Notes: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Title != title || cleared.Notes != "" {
		t.Fatalf("clear notes: %+v", cleared)
	}
	if cleared.ID != created.ID || cleared.OwnerID != created.OwnerID || !cleared.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("immutable fields changed: before=%+v after=%+v", created, cleared)
	}
	if _, err := repo.Update(ctx, bob, created.ID, UpdateFields{Title: &empty}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign update: %v", err)
	}
	if _, err := repo.Update(ctx, alice, uuid.New(), UpdateFields{Title: &empty}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown update: %v", err)
	}
	// Reading through a separate pool confirms persistence independently of
	// the connection used for the update.
	secondDB := openIntegrationDB(t)
	got, err := NewPostgresRepository(secondDB).GetByID(ctx, alice, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != cleared.ID || got.OwnerID != cleared.OwnerID || got.Title != cleared.Title || got.Notes != cleared.Notes || !got.CreatedAt.Equal(cleared.CreatedAt) {
		t.Fatalf("persisted record changed: %+v", got)
	}
}

func TestPostgresRepositoryUpdateCancelledContextIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	owner := createIntegrationOwner(t, db)
	repo := NewPostgresRepository(db)
	created, err := repo.Create(integrationContext(t), owner, "Original", "Notes")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	title := "Changed"
	if _, err := repo.Update(ctx, owner, created.ID, UpdateFields{Title: &title}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled update: %v", err)
	}
	got, err := repo.GetByID(integrationContext(t), owner, created.ID)
	if err != nil || got.Title != created.Title {
		t.Fatalf("cancelled update modified trip: %+v, %v", got, err)
	}
}
