package appuser

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestIdentityFromContext(t *testing.T) {
	t.Run("missing identity", func(t *testing.T) {
		_, ok := IdentityFromContext(context.Background())
		if ok {
			t.Fatal("expected no application identity")
		}
	})

	t.Run("valid identity", func(t *testing.T) {
		want := Identity{ID: uuid.New()}

		ctx := WithIdentity(context.Background(), want)
		got, ok := IdentityFromContext(ctx)

		if !ok {
			t.Fatal("expected application identity")
		}
		if got != want {
			t.Fatalf("got identity %+v, want %+v", got, want)
		}
	})

	t.Run("zero UUID is not a resolved identity", func(t *testing.T) {
		ctx := WithIdentity(context.Background(), Identity{})

		_, ok := IdentityFromContext(ctx)
		if ok {
			t.Fatal("expected zero UUID to be rejected")
		}
	})

	t.Run("unrelated context values are ignored", func(t *testing.T) {
		type unrelatedKey struct{}

		ctx := context.WithValue(
			context.Background(),
			unrelatedKey{},
			Identity{ID: uuid.New()},
		)

		_, ok := IdentityFromContext(ctx)
		if ok {
			t.Fatal("expected unrelated context value to be ignored")
		}
	})
}
