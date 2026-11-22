// Package shares stores link and public shares: fixed selections of photos
// from one trip that people without access to the trip may view (design doc
// 4.2, 5.3). Recipients only ever see coarse positions, never exact ones.
package shares

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Share modes; see ShareMode in the OpenAPI contract.
const (
	ModeLink   = "link"
	ModePublic = "public"
)

var (
	// ErrNotFound is returned for an absent share, a share of another owner,
	// or a trip the caller does not own. The cases are indistinguishable.
	ErrNotFound = errors.New("share not found")
	// ErrRevoked is returned when a recipient opens a revoked link.
	ErrRevoked = errors.New("share revoked")
	// ErrInvalidPhotos is returned when a selected photo is not a processed
	// photo of the share's trip.
	ErrInvalidPhotos = errors.New("photos are not shareable photos of this trip")
)

// Share is one share as its owner manages it.
type Share struct {
	ID          uuid.UUID
	TripID      uuid.UUID
	Mode        string
	Title       string
	Description *string
	Token       string
	PhotoIDs    []uuid.UUID
	CreatedAt   time.Time
	RevokedAt   *time.Time
}

// Candidate is a photo the owner selected, with what the coarse position
// needs. Only processed photos with a display image are candidates.
type Candidate struct {
	ID      uuid.UUID
	Lat     *float64
	Lng     *float64
	City    *string
	Country *string
}

// Selected is one selected photo with the coarse position recipients see.
type Selected struct {
	PhotoID    uuid.UUID
	DisplayLat *float64
	DisplayLng *float64
}

// NewShare is the input of Create.
type NewShare struct {
	TripID      uuid.UUID
	Mode        string
	Title       string
	Description *string
	Photos      []Selected
}

// SharedPhoto is one photo as a recipient sees it: no exact position, no EXIF.
type SharedPhoto struct {
	ID                uuid.UUID
	DisplayObjectName string
	CaptureTimeLocal  *string
	City              *string
	Country           *string
	DisplayLat        *float64
	DisplayLng        *float64
}

// Collection is what a recipient of one share sees.
type Collection struct {
	ShareID     uuid.UUID
	Title       string
	Description *string
	CreatedAt   time.Time
	Photos      []SharedPhoto
}

// PublicPoint is one photo of a live public share at its coarse position,
// the input of public-map clustering.
type PublicPoint struct {
	PhotoID           uuid.UUID
	ShareID           uuid.UUID
	ShareTitle        string
	DisplayObjectName string
	Lat, Lng          float64
	City, Country     *string
}

// Repository is the storage boundary for shares. ownerID must be a trusted,
// server-resolved application-user ID.
type Repository interface {
	// Candidates returns the caller's processed photos of tripID among ids.
	// It returns ErrNotFound when the caller does not own the trip.
	Candidates(ctx context.Context, ownerID, tripID uuid.UUID, ids []uuid.UUID) ([]Candidate, error)
	// Create stores a share of the caller's trip with a new token.
	Create(ctx context.Context, ownerID uuid.UUID, share NewShare) (Share, error)
	// ListByTrip returns the trip's unrevoked shares, newest first.
	ListByTrip(ctx context.Context, ownerID, tripID uuid.UUID) ([]Share, error)
	// Revoke turns a share off; revoking twice is not an error.
	Revoke(ctx context.Context, ownerID, shareID uuid.UUID) error
	// ByToken returns the collection behind a link token, ErrNotFound or ErrRevoked.
	ByToken(ctx context.Context, token string) (Collection, error)
	// PublicByID returns a live public share's collection or ErrNotFound.
	PublicByID(ctx context.Context, shareID uuid.UUID) (Collection, error)
	// PublicPoints returns photos of live public shares whose coarse position
	// is inside the box (west > east crosses the antimeridian), at most limit.
	PublicPoints(ctx context.Context, west, south, east, north float64, limit int) ([]PublicPoint, error)
}

// NewToken returns 128 random bits as unpadded base64url (22 characters).
func NewToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
