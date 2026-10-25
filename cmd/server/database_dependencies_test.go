package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// A closed pool gives a deterministic way to verify that both repositories
// use the supplied pool, without requiring a running PostgreSQL instance.
func TestDatabaseDependenciesReusePool(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://localhost/mapmory?sslmode=disable")
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}

	deps := newDatabaseDependencies(db)
	if deps.users == nil || deps.trips == nil {
		t.Fatal("expected both repositories")
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close pool: %v", err)
	}

	ctx := context.Background()
	_, userErr := deps.users.FindOrCreateByAuthSubject(ctx, "test-subject")
	if userErr == nil {
		t.Fatal("user repository unexpectedly worked after pool close")
	}
	_, tripErr := deps.trips.ListByOwner(ctx, uuid.New())
	if tripErr == nil {
		t.Fatal("trip repository unexpectedly worked after pool close")
	}
}
