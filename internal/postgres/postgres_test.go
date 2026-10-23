package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOpenRejectsEmptyConnectionString(t *testing.T) {
	for _, dsn := range []string{"", "   "} {
		t.Run("dsn="+dsn, func(t *testing.T) {
			db, err := Open(context.Background(), dsn)

			if err == nil {
				if db != nil {
					db.Close()
				}
				t.Fatal("expected an error for empty connection string")
			}
			if db != nil {
				db.Close()
				t.Fatal("expected no database pool")
			}
			if !strings.Contains(err.Error(), "connection string is required") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestOpenRespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db, err := Open(ctx, "postgres://localhost/mapmory")

	if db != nil {
		db.Close()
		t.Fatal("expected no database pool")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}
