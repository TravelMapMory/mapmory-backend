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

// Place-label states; see LabelStatus in the OpenAPI contract. They change
// independently of the photo state, so a geocoding failure never blocks a photo.
const (
	LabelPending   = "pending"
	LabelSuggested = "suggested"
	LabelConfirmed = "confirmed"
)

// CaptureTimeLayout is the wall-clock format of capture_time_local.
const CaptureTimeLayout = "2006-01-02T15:04:05"

// ErrNotFound is returned when a photo, batch or trip is absent or not owned
// by the caller. The two cases are deliberately indistinguishable.
var ErrNotFound = errors.New("photo not found")

// ErrNotProcessed is returned when a location change targets a photo that is
// still uploading or processing, or that failed.
var ErrNotProcessed = errors.New("photo is not processed")

// ErrNoSuggestion is returned when an owner confirms labels that are not in
// the suggested state.
var ErrNoSuggestion = errors.New("photo has no suggested labels")

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
	Country           *string
	City              *string
	PlaceName         *string
	LabelStatus       string
	LabelProvider     *string
	LabelAttribution  *string
}

// Labels are normalized place labels and the credit their provider requires.
// Empty strings are stored as NULL.
type Labels struct {
	Country     string
	City        string
	PlaceName   string
	Provider    string
	Attribution string
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

// Cursor marks the last photo of a page in capture order. CaptureTimeLocal
// is nil for undated photos and UploadedAt for never-uploaded ones; both sort
// after every photo that has the value.
type Cursor struct {
	CaptureTimeLocal *string    `json:"c,omitempty"`
	UploadedAt       *time.Time `json:"u,omitempty"`
	ID               uuid.UUID  `json:"i"`
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
	// PostgreSQL has no year 0, so a hand-made cursor with one must be a 400
	// rather than reaching the query and failing as a 500.
	if cursor.CaptureTimeLocal != nil {
		if t, err := time.Parse(CaptureTimeLayout, *cursor.CaptureTimeLocal); err != nil || t.Year() < 1 {
			return Cursor{}, ErrInvalidCursor
		}
	}
	if cursor.UploadedAt != nil && cursor.UploadedAt.UTC().Year() < 1 {
		return Cursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

// CursorAfter returns the cursor that continues after photo.
func CursorAfter(photo Photo) Cursor {
	return Cursor{CaptureTimeLocal: photo.CaptureTimeLocal, UploadedAt: photo.UploadedAt, ID: photo.ID}
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
	// Get returns one of the caller's photos.
	Get(ctx context.Context, ownerID, photoID uuid.UUID) (Photo, error)
	// GetBatch returns the caller's batch and all of its photos.
	GetBatch(ctx context.Context, ownerID, batchID uuid.UUID) (tripID uuid.UUID, photos []Photo, err error)
	// ListByTrip returns up to limit of the caller's trip photos after cursor
	// in capture order (#45): camera-local capture time oldest first, then
	// upload time oldest first, then ID. Undated and never-uploaded photos
	// sort last. It returns ErrNotFound for a trip the caller lacks.
	ListByTrip(ctx context.Context, ownerID, tripID uuid.UUID, after *Cursor, limit int) ([]Photo, error)
	// SuggestLabels stores resolver labels for a photo whose position is still
	// lat, lng and whose labels are not confirmed. It reports whether it
	// stored them, so a correction made meanwhile is never overwritten.
	SuggestLabels(ctx context.Context, photoID uuid.UUID, lat, lng float64, labels Labels) (bool, error)
	// Relocate sets the caller's processed photo to lat, lng (never touching
	// the EXIF pair or capture time) and makes it ready. With labels the owner's
	// choice is confirmed; without them, labels are cleared and pending.
	// It returns ErrNotFound or ErrNotProcessed.
	Relocate(ctx context.Context, ownerID, photoID uuid.UUID, lat, lng float64, labels *Labels) (Photo, error)
	// ConfirmLabels accepts the caller's suggested labels. It returns
	// ErrNotFound or ErrNoSuggestion.
	ConfirmLabels(ctx context.Context, ownerID, photoID uuid.UUID) (Photo, error)
	// ClaimLabelRetries returns up to limit of the caller's processed, located
	// photos in tripID whose labels are still pending after fewer than
	// MaxLabelRetries retries, and counts one retry for each (design doc 7.3).
	ClaimLabelRetries(ctx context.Context, ownerID, tripID uuid.UUID, limit int) ([]Photo, error)
}

// MaxLabelRetries is how often pending labels are retried after upload.
const MaxLabelRetries = 3
