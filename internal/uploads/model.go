// Package uploads registers upload batches and the photo records they create.
package uploads

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// MaxFileBytes is the agreed 25 MB limit, in decimal bytes (backend #5).
const MaxFileBytes int64 = 25_000_000

// MaxFilesPerBatch matches RegisterUpload.files.maxItems in the contract.
const MaxFilesPerBatch = 100

// ErrTripNotFound is returned when the trip is absent or not owned by the
// caller. The two cases are deliberately indistinguishable.
var ErrTripNotFound = errors.New("trip not found")

// File is one accepted file of a batch. Its photo ID and object name are
// chosen by the server before the upload URL is signed.
type File struct {
	PhotoID      uuid.UUID
	ClientFileID string
	ContentType  string
	SizeBytes    int64
	ObjectName   string
}

// Repository stores batches and their pending photo records.
// The caller must supply a trusted, server-resolved application-user ID.
type Repository interface {
	// CreateBatch atomically checks that ownerID owns tripID and inserts the
	// batch with its files. It returns ErrTripNotFound otherwise.
	CreateBatch(ctx context.Context, ownerID, tripID, batchID uuid.UUID, files []File) error
}

// NormalizeContentType returns the canonical supported content type, or ""
// when the type is not JPEG or PNG.
func NormalizeContentType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	default:
		return ""
	}
}

// ObjectName is the private GCS object that holds a photo's original bytes.
func ObjectName(tripID, photoID uuid.UUID) string {
	return "originals/" + tripID.String() + "/" + photoID.String()
}
