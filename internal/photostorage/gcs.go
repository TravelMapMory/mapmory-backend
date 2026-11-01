// Package photostorage signs direct browser uploads to the private photo bucket.
package photostorage

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"cloud.google.com/go/storage"
)

// UploadTTL is how long a signed upload URL stays valid.
const UploadTTL = 15 * time.Minute

// Upload is a signed request the browser sends to GCS without its Firebase token.
type Upload struct {
	Method    string
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// Signer issues signed upload requests for single objects.
type Signer interface {
	SignUpload(objectName, contentType string, sizeBytes int64) (Upload, error)
}

// GCS signs V4 URLs without a service-account key, through the IAM
// Credentials signBlob API. The signing account needs Token Creator on itself
// (on the VM) or the developer needs it on that account (locally).
type GCS struct {
	client *storage.Client
	bucket *storage.BucketHandle
	signer string
	now    func() time.Time
}

// NewGCS uses Application Default Credentials. signerEmail may be empty on
// GCE, where the VM's service account is detected; locally it must name the
// service account whose identity signs the URLs.
func NewGCS(ctx context.Context, bucket, signerEmail string) (*GCS, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create storage client: %w", err)
	}
	return &GCS{
		client: client,
		bucket: client.Bucket(bucket),
		signer: signerEmail,
		now:    time.Now,
	}, nil
}

// Close releases the storage client.
func (g *GCS) Close() error {
	return g.client.Close()
}

// SignUpload signs a PUT that GCS accepts only with the given content type
// and exactly sizeBytes of body.
func (g *GCS) SignUpload(objectName, contentType string, sizeBytes int64) (Upload, error) {
	expires := g.now().Add(UploadTTL).UTC().Truncate(time.Second)
	lengthRange := strconv.FormatInt(sizeBytes, 10) + "," + strconv.FormatInt(sizeBytes, 10)

	url, err := g.bucket.SignedURL(objectName, &storage.SignedURLOptions{
		GoogleAccessID: g.signer,
		Scheme:         storage.SigningSchemeV4,
		Method:         http.MethodPut,
		Expires:        expires,
		ContentType:    contentType,
		Headers:        []string{"x-goog-content-length-range:" + lengthRange},
	})
	if err != nil {
		return Upload{}, fmt.Errorf("sign upload URL: %w", err)
	}
	return Upload{
		Method: http.MethodPut,
		URL:    url,
		Headers: map[string]string{
			"Content-Type":                contentType,
			"x-goog-content-length-range": lengthRange,
		},
		ExpiresAt: expires,
	}, nil
}
