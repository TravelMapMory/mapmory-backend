package trips

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryRepository is an owner-scoped test double, not durable storage.
type MemoryRepository struct {
	mu    sync.RWMutex
	trips []Trip
}

// NewMemoryRepository returns an empty, concurrency-safe repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{trips: make([]Trip, 0)}
}

var _ Repository = (*MemoryRepository)(nil)

func (r *MemoryRepository) Create(ctx context.Context, ownerID uuid.UUID, title, notes string) (Trip, error) {
	if err := ctx.Err(); err != nil {
		return Trip{}, err
	}
	t := Trip{
		ID: uuid.New(), OwnerID: ownerID, Title: title, Notes: notes,
		CreatedAt: time.Now().UTC(),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Trip{}, err
	}
	r.trips = append(r.trips, t)
	return t, nil
}

func (r *MemoryRepository) ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]Trip, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Trip, 0)
	for _, t := range r.trips {
		if t.OwnerID == ownerID {
			result = append(result, t)
		}
	}
	return result, nil
}

func (r *MemoryRepository) GetByID(ctx context.Context, ownerID, tripID uuid.UUID) (Trip, error) {
	if err := ctx.Err(); err != nil {
		return Trip{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.trips {
		if t.ID == tripID && t.OwnerID == ownerID {
			return t, nil
		}
	}
	return Trip{}, ErrNotFound
}

func (r *MemoryRepository) Update(
	ctx context.Context,
	ownerID, tripID uuid.UUID,
	fields UpdateFields,
) (Trip, error) {
	if err := ctx.Err(); err != nil {
		return Trip{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range r.trips {
		record := &r.trips[i]

		if record.ID != tripID || record.OwnerID != ownerID {
			continue
		}

		if fields.Title != nil {
			record.Title = *fields.Title
		}
		if fields.Notes != nil {
			record.Notes = *fields.Notes
		}

		return *record, nil
	}

	return Trip{}, ErrNotFound
}
