// Package photos stores the photo lifecycle after registration: upload
// completion, processing results and owner listing.
package photos

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Photo states; see PhotoState in the OpenAPI contract.
const (
	StatePending       = "pending"
	StateProcessing    = "processing"
	StateReady         = "ready"
	StateNeedsLocation = "needs-location"
	StateFailed        = "failed"
)

// CaptureTimeLayout is the wall-clock format of capture_time_local.
const CaptureTimeLayout = "2006-01-02T15:04:05"

// ErrNotFound is returned when a photo, batch or trip is absent or not owned
// by the caller. The two cases are deliberately indistinguishable.
var ErrNotFound = errors.New("photo not found")

// Photo is one persisted photo. Pointer fields are NULL when unknown.
type Photo struct {
	ID                uuid.UUID
	TripID            uuid.UUID
	BatchID           uuid.UUID
	ClientFileID      string
	State             string
	ErrorCode         *string
	ObjectName        string
	ContentType       string
	SizeBytes         int64
	UploadedAt        *time.Time
	ProcessingStarted *time.Time
	CaptureTimeLocal  *string
	CaptureUTCOffset  *string
	ExifLat           *float64
	ExifLng           *float64
	Lat               *float64
	Lng               *float64
	DisplayObjectName *string
	CreatedAt         time.Time
}

// Processed is the successful outcome of processing one upload.
type Processed struct {
	UploadedAt        time.Time
	CaptureTimeLocal  *string
	CaptureUTCOffset  *string
	ExifLat           *float64
	ExifLng           *float64
	DisplayObjectName string
}

// Cursor marks the last photo of a page. UploadedAt is nil for photos that
// were never uploaded, which sort after all uploaded ones.
type Cursor struct {
	UploadedAt *time.Time `json:"u,omitempty"`
	ID         uuid.UUID  `json:"i"`
}

// ErrInvalidCursor is returned by DecodeCursor for a cursor it did not issue.
var ErrInvalidCursor = errors.New("invalid cursor")

// Encode returns the opaque next_cursor value.
func (c Cursor) Encode() string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeCursor parses a value produced by Encode.
func DecodeCursor(value string) (Cursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var cursor Cursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.ID == uuid.Nil {
		return Cursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

// CursorAfter returns the cursor that continues after photo.
func CursorAfter(photo Photo) Cursor {
	return Cursor{UploadedAt: photo.UploadedAt, ID: photo.ID}
}

// Repository is the storage boundary for photo lifecycle operations.
// ownerID must be a trusted, server-resolved application-user ID.
type Repository interface {
	// GetByClientFile returns the caller's photo registered in a batch.
	GetByClientFile(ctx context.Context, ownerID, batchID uuid.UUID, clientFileID string) (Photo, error)
	// Claim moves a pending photo, or one whose processing started before
	// staleBefore, to processing. It reports whether this caller won.
	Claim(ctx context.Context, photoID uuid.UUID, staleBefore time.Time) (bool, error)
	// Release returns a photo this caller claimed to pending, for a retry.
	Release(ctx context.Context, photoID uuid.UUID) error
	// Fail records a claimed photo as failed for a reason the image caused.
	Fail(ctx context.Context, photoID uuid.UUID, uploadedAt time.Time, code string) (Photo, error)
	// Finish records a claimed photo's processing results.
	Finish(ctx context.Context, photoID uuid.UUID, result Processed) (Photo, error)
	// GetBatch returns the caller's batch and all of its photos.
	GetBatch(ctx context.Context, ownerID, batchID uuid.UUID) (tripID uuid.UUID, photos []Photo, err error)
	// ListByTrip returns up to limit of the caller's trip photos after cursor,
	// newest upload first and never-uploaded photos last, ID descending as
	// the tie-breaker. It returns ErrNotFound for a trip the caller lacks.
	ListByTrip(ctx context.Context, ownerID, tripID uuid.UUID, after *Cursor, limit int) ([]Photo, error)
}
