package trips

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a trip is absent or not owned by the caller.
// Repositories must not distinguish those cases for GetByID.
var ErrNotFound = errors.New("trip not found")

// UpdateFields represents a partial update.
// nil means unchanged; a pointer to "" means explicitly clear the field.
type UpdateFields struct {
	Title *string
	Notes *string
}

// Repository is the storage boundary for owner-scoped trip operations.
// The caller must supply a trusted, server-resolved application-user ID.
type Repository interface {
	Create(
		ctx context.Context,
		ownerID uuid.UUID,
		title string,
		notes string,
	) (Trip, error)

	ListByOwner(
		ctx context.Context,
		ownerID uuid.UUID,
	) ([]Trip, error)

	GetByID(
		ctx context.Context,
		ownerID uuid.UUID,
		tripID uuid.UUID,
	) (Trip, error)

	Update(
		ctx context.Context,
		ownerID uuid.UUID,
		tripID uuid.UUID,
		fields UpdateFields,
	) (Trip, error)
}
