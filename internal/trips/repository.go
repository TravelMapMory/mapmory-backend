package trips

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a trip is absent or not owned by the caller.
// Repositories must not distinguish those cases for GetByID.
var ErrNotFound = errors.New("trip not found")

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
}
