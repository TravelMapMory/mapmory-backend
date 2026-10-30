//go:build integration

package postgres

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"
)

// integrationDatabaseURL returns the connection string provided by the
// isolated test harness. Integration tests must not fall back to the
// application's normal DATABASE_URL.
func integrationDatabaseURL(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("MAPMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("MAPMORY_TEST_DATABASE_URL is required for integration tests")
	}
	return dsn
}

func TestOpenIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := Open(ctx, integrationDatabaseURL(t))
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Verify that PostgreSQL can execute a real query through the pool.
	var result int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&result); err != nil {
		t.Fatalf("SELECT 1 failed: %v", err)
	}
	if result != 1 {
		t.Fatalf("SELECT 1 returned %d, want 1", result)
	}

	// These settings should be applied to the shared connection pool.
	stats := db.Stats()
	if stats.MaxOpenConnections != maxOpenConnections {
		t.Errorf(
			"max open connections = %d, want %d",
			stats.MaxOpenConnections,
			maxOpenConnections,
		)
	}

	// Confirm that Open returns an ordinary database/sql pool that
	// callers can reuse for multiple queries.
	var databaseName string
	if err := db.QueryRowContext(
		ctx,
		"SELECT current_database()",
	).Scan(&databaseName); err != nil {
		t.Fatalf("query database name: %v", err)
	}
	if databaseName != "mapmory_test" {
		t.Errorf("database name = %q, want mapmory_test", databaseName)
	}
}

func TestOpenRejectsUnknownDatabase(t *testing.T) {
	dsn := integrationDatabaseURL(t)

	// Change only the database name. The server and credentials remain
	// valid, so this checks a genuine server-side connection failure.
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	parsed.Path = "/mapmory_missing_database"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := Open(ctx, parsed.String())
	if db != nil {
		_ = db.Close()
		t.Fatal("Open() returned a pool for a nonexistent database")
	}
	if err == nil {
		t.Fatal("Open() succeeded for a nonexistent database")
	}
}

func TestOpenReturnsUsableSQLPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := Open(ctx, integrationDatabaseURL(t))
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer db.Close()

	// Reuse the same pool for several operations. A *sql.DB is a pool,
	// not a dedicated connection tied to one query or request.
	for i := 0; i < 3; i++ {
		if err := db.PingContext(ctx); err != nil {
			t.Fatalf("ping %d failed: %v", i+1, err)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close pool: %v", err)
	}
	if err := db.PingContext(ctx); err == nil {
		t.Fatal("PingContext succeeded after Close")
	}
}
