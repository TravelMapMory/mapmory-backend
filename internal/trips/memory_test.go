package trips

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestMemoryRepositoryOwnerIsolation(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	alice, bob := uuid.New(), uuid.New()

	first, err := repo.Create(ctx, alice, "Japan", "Kyoto")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == uuid.Nil || first.OwnerID != alice || first.CreatedAt.IsZero() || first.Title != "Japan" || first.Notes != "Kyoto" {
		t.Fatalf("unexpected created trip: %+v", first)
	}
	second, err := repo.Create(ctx, bob, "Finland", "Espoo")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("trip IDs must be unique")
	}

	for _, tc := range []struct {
		name  string
		owner uuid.UUID
		want  uuid.UUID
	}{
		{"alice", alice, first.ID}, {"bob", bob, second.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listed, err := repo.ListByOwner(ctx, tc.owner)
			if err != nil {
				t.Fatal(err)
			}
			if len(listed) != 1 || listed[0].ID != tc.want {
				t.Fatalf("list: %+v", listed)
			}
			found, err := repo.GetByID(ctx, tc.owner, tc.want)
			if err != nil || found.ID != tc.want {
				t.Fatalf("get: %+v, %v", found, err)
			}
		})
	}
	if _, err := repo.GetByID(ctx, alice, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign trip: got %v, want ErrNotFound", err)
	}
	if _, err := repo.GetByID(ctx, alice, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown trip: got %v, want ErrNotFound", err)
	}
	empty, err := repo.ListByOwner(ctx, uuid.New())
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list: %v, %v", empty, err)
	}
}

func TestMemoryRepositoryCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewMemoryRepository()
	owner := uuid.New()
	if _, err := repo.Create(ctx, owner, "test", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("create: %v", err)
	}
	if _, err := repo.ListByOwner(ctx, owner); !errors.Is(err, context.Canceled) {
		t.Fatalf("list: %v", err)
	}
	if _, err := repo.GetByID(ctx, owner, uuid.New()); !errors.Is(err, context.Canceled) {
		t.Fatalf("get: %v", err)
	}
	got, err := repo.ListByOwner(context.Background(), owner)
	if err != nil || len(got) != 0 {
		t.Fatalf("cancelled create mutated repository: %v, %v", got, err)
	}
}

func TestMemoryRepositoryConcurrentCreate(t *testing.T) {
	repo := NewMemoryRepository()
	owner := uuid.New()
	const count = 50
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Create(context.Background(), owner, "trip", "")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.ListByOwner(context.Background(), owner)
	if err != nil || len(got) != count {
		t.Fatalf("got %d trips, error %v; want %d", len(got), err, count)
	}
}
