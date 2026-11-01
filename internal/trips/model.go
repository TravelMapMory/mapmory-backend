// Package trips defines trip persistence models and contracts.
package trips

import (
	"time"

	"github.com/google/uuid"
)

// Trip is a persisted trip. API-only fields (such as photo_count and cover)
// are intentionally not part of the persistence model.
type Trip struct {
	ID        uuid.UUID
	OwnerID   uuid.UUID
	Title     string
	Notes     string
	CreatedAt time.Time
}
