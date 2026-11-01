// Package photostorage reads, writes and signs URLs for objects in the
// private photo bucket.
package photostorage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
)

// UploadTTL is how long a signed upload URL stays valid.
const UploadTTL = 15 * time.Minute

// DisplayTTL is how long a signed display-image URL stays valid. A cached URL
// is reused while at least displayMinRemaining of it is left.
const (
	DisplayTTL          = time.Hour
	displayMinRemaining = 30 * time.Minute
)

// ErrObjectNotFound is returned when the object does not exist.
var ErrObjectNotFound = errors.New("object not found")

// ErrObjectTooLarge is returned by Read when the object exceeds its limit.
var ErrObjectTooLarge = errors.New("object too large")

// Upload is a signed request the browser sends to GCS without its Firebase token.
type Upload struct {
	Method    string
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Size        int64
	ContentType string
	Created     time.Time
}

// Signer issues signed upload requests for single objects.
type Signer interface {
	SignUpload(objectName, contentType string, sizeBytes int64) (Upload, error)
}

// Store is everything the photo lifecycle needs from object storage.
type Store interface {
	Signer
	Stat(ctx context.Context, objectName string) (ObjectInfo, error)
	Read(ctx context.Context, objectName string, limit int64) ([]byte, error)
	Write(ctx context.Context, objectName, contentType string, data []byte) error
	SignDisplay(objectName string) (url string, expiresAt time.Time, err error)
}

// GCS implements Store. URLs are signed without a service-account key,
// through the IAM Credentials signBlob API.
type GCS struct {
	client *storage.Client
	bucket *storage.BucketHandle
	signer string
	now    func() time.Time

	mu      sync.Mutex
	display map[string]signedURL
}

type signedURL struct {
	url     string
	expires time.Time
}

var _ Store = (*GCS)(nil)

// NewGCS uses Application Default Credentials. On the VM, serviceAccount is
// empty and the VM's own account is used. Locally it names the account the
// backend acts as (mapmory-vm), so a developer only needs Token Creator on it,
// not access to the bucket.
func NewGCS(ctx context.Context, bucket, serviceAccount string) (*GCS, error) {
	var options []option.ClientOption
	if serviceAccount != "" {
		tokens, err := impersonate.CredentialsTokenSource(ctx, impersonate.CredentialsConfig{
			TargetPrincipal: serviceAccount,
			Scopes:          []string{"https://www.googleapis.com/auth/cloud-platform"},
		})
		if err != nil {
			return nil, fmt.Errorf("impersonate %s: %w", serviceAccount, err)
		}
		options = append(options, option.WithTokenSource(tokens))
	}
	client, err := storage.NewClient(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("create storage client: %w", err)
	}
	return &GCS{
		client:  client,
		bucket:  client.Bucket(bucket),
		signer:  serviceAccount,
		now:     time.Now,
		display: make(map[string]signedURL),
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

// SignDisplay signs a GET for a display image, reusing a recent signature so
// listing many photos costs few IAM calls.
func (g *GCS) SignDisplay(objectName string) (string, time.Time, error) {
	now := g.now()
	g.mu.Lock()
	cached, ok := g.display[objectName]
	g.mu.Unlock()
	if ok && cached.expires.Sub(now) >= displayMinRemaining {
		return cached.url, cached.expires, nil
	}

	expires := now.Add(DisplayTTL).UTC().Truncate(time.Second)
	url, err := g.bucket.SignedURL(objectName, &storage.SignedURLOptions{
		GoogleAccessID: g.signer,
		Scheme:         storage.SigningSchemeV4,
		Method:         http.MethodGet,
		Expires:        expires,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign display URL: %w", err)
	}
	g.mu.Lock()
	for name, entry := range g.display {
		if entry.expires.Sub(now) < displayMinRemaining {
			delete(g.display, name)
		}
	}
	g.display[objectName] = signedURL{url: url, expires: expires}
	g.mu.Unlock()
	return url, expires, nil
}

// Stat returns the object's size, content type and creation time.
func (g *GCS) Stat(ctx context.Context, objectName string) (ObjectInfo, error) {
	attrs, err := g.bucket.Object(objectName).Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return ObjectInfo{}, ErrObjectNotFound
	}
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat object: %w", err)
	}
	return ObjectInfo{Size: attrs.Size, ContentType: attrs.ContentType, Created: attrs.Created}, nil
}

// Read returns the whole object, or ErrObjectTooLarge if it exceeds limit.
func (g *GCS) Read(ctx context.Context, objectName string, limit int64) ([]byte, error) {
	reader, err := g.bucket.Object(objectName).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open object: %w", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, ErrObjectTooLarge
	}
	return data, nil
}

// Write stores data as a new version of the object.
func (g *GCS) Write(ctx context.Context, objectName, contentType string, data []byte) error {
	writer := g.bucket.Object(objectName).NewWriter(ctx)
	writer.ContentType = contentType
	writer.CacheControl = "private, max-age=3600"
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write object: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("write object: %w", err)
	}
	return nil
}
