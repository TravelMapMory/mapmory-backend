package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/photostorage"
	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/TravelMapMory/mapmory-backend/internal/uploads"
	"github.com/google/uuid"
)

type batchRepositoryFake struct {
	calls   int
	ownerID uuid.UUID
	tripID  uuid.UUID
	batchID uuid.UUID
	files   []uploads.File
	err     error
}

func (f *batchRepositoryFake) CreateBatch(_ context.Context, ownerID, tripID, batchID uuid.UUID, files []uploads.File) error {
	f.calls++
	f.ownerID, f.tripID, f.batchID, f.files = ownerID, tripID, batchID, files
	return f.err
}

// storeFake keeps objects in memory and signs fake URLs.
type storeFake struct {
	calls   int
	err     error
	objects map[string]storedObject
	writes  int
	reads   int
}

type storedObject struct {
	data        []byte
	contentType string
	created     time.Time
}

func (s *storeFake) SignUpload(object, contentType string, size int64) (photostorage.Upload, error) {
	s.calls++
	if s.err != nil {
		return photostorage.Upload{}, s.err
	}
	return photostorage.Upload{
		Method:    http.MethodPut,
		URL:       "https://storage.googleapis.com/bucket/" + object,
		Headers:   map[string]string{"Content-Type": contentType, "x-goog-content-length-range": fmt.Sprintf("%d,%d", size, size)},
		ExpiresAt: time.Date(2026, 11, 1, 12, 15, 0, 0, time.UTC),
	}, nil
}

func (s *storeFake) put(object, contentType string, data []byte) {
	if s.objects == nil {
		s.objects = make(map[string]storedObject)
	}
	s.objects[object] = storedObject{data: data, contentType: contentType, created: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
}

func (s *storeFake) Stat(_ context.Context, object string) (photostorage.ObjectInfo, error) {
	stored, ok := s.objects[object]
	if !ok {
		return photostorage.ObjectInfo{}, photostorage.ErrObjectNotFound
	}
	return photostorage.ObjectInfo{Size: int64(len(stored.data)), ContentType: stored.contentType, Created: stored.created}, nil
}

func (s *storeFake) Read(_ context.Context, object string, limit int64) ([]byte, error) {
	s.reads++
	stored, ok := s.objects[object]
	if !ok {
		return nil, photostorage.ErrObjectNotFound
	}
	if int64(len(stored.data)) > limit {
		return nil, photostorage.ErrObjectTooLarge
	}
	return stored.data, nil
}

func (s *storeFake) Write(_ context.Context, object, contentType string, data []byte) error {
	s.writes++
	if s.err != nil {
		return s.err
	}
	s.put(object, contentType, data)
	return nil
}

func (s *storeFake) SignDisplay(object string) (string, time.Time, error) {
	if s.err != nil {
		return "", time.Time{}, s.err
	}
	return "https://storage.googleapis.com/bucket/" + object + "?signed", time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC), nil
}

// uploadTestHandler serves one trip owned by owner; any other trip is absent.
func uploadTestHandler(owner, ownedTrip uuid.UUID, batches *batchRepositoryFake, signer *storeFake) http.Handler {
	verifier := endpointVerifierFunc(func(_ context.Context, token string) (*firebaseauth.Token, error) {
		if token != "valid" {
			return nil, errors.New("invalid token")
		}
		return &firebaseauth.Token{UID: "subject-a"}, nil
	})
	resolver := endpointResolverFunc(func(context.Context, string) (uuid.UUID, error) { return owner, nil })
	tripRepo := tripRepositoryFake{get: func(_ context.Context, o, id uuid.UUID) (trips.Trip, error) {
		if o != owner || id != ownedTrip {
			return trips.Trip{}, trips.ErrNotFound
		}
		return trips.Trip{ID: id, OwnerID: o}, nil
	}}
	return newPersistentHandler(verifier, resolver, tripRepo,
		newUploadHandlers(tripRepo, batches, nil, signer))
}

func TestRegisterBatchAcceptsAndRejectsPerFile(t *testing.T) {
	owner, tripID := uuid.New(), uuid.New()
	batches, signer := &batchRepositoryFake{}, &storeFake{}
	h := uploadTestHandler(owner, tripID, batches, signer)

	body := `{"files":[
		{"client_file_id":"a","size_bytes":1200,"content_type":"image/jpeg"},
		{"client_file_id":"b","size_bytes":10,"content_type":"image/heic"},
		{"client_file_id":"c","size_bytes":25000001,"content_type":"image/png"},
		{"client_file_id":"d","size_bytes":25000000,"content_type":"IMAGE/PNG"}
	]}`
	response := tripRequest(h, "POST", "/api/trips/"+tripID.String()+"/batches", body, "Bearer valid")
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var got registeredBatch
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if len(got.AcceptedFiles) != 2 || got.AcceptedFiles[0].ClientFileID != "a" || got.AcceptedFiles[1].ClientFileID != "d" {
		t.Fatalf("accepted = %+v", got.AcceptedFiles)
	}
	if len(got.RejectedFiles) != 2 ||
		got.RejectedFiles[0].ClientFileID != "b" || got.RejectedFiles[0].Error.Code != "unsupported_type" ||
		got.RejectedFiles[1].ClientFileID != "c" || got.RejectedFiles[1].Error.Code != "file_too_large" {
		t.Fatalf("rejected = %+v", got.RejectedFiles)
	}
	if signer.calls != 2 || batches.calls != 1 {
		t.Fatalf("signer calls=%d batch calls=%d", signer.calls, batches.calls)
	}
	if batches.ownerID != owner || batches.tripID != tripID || batches.batchID.String() != got.BatchID {
		t.Fatalf("stored owner=%s trip=%s batch=%s", batches.ownerID, batches.tripID, batches.batchID)
	}
	for i, file := range batches.files {
		accepted := got.AcceptedFiles[i]
		if file.PhotoID.String() != accepted.PhotoID || file.ClientFileID != accepted.ClientFileID {
			t.Fatalf("stored file %+v does not match response %+v", file, accepted)
		}
		if want := uploads.ObjectName(tripID, file.PhotoID); file.ObjectName != want {
			t.Fatalf("object = %q want %q", file.ObjectName, want)
		}
		if accepted.Upload.Method != "PUT" || accepted.Upload.Headers["Content-Type"] != file.ContentType {
			t.Fatalf("upload target = %+v", accepted.Upload)
		}
	}
	if batches.files[1].ContentType != "image/png" || batches.files[1].SizeBytes != 25_000_000 {
		t.Fatalf("second file = %+v", batches.files[1])
	}
}

func TestRegisterBatchAllRejectedStillCreatesEmptyBatch(t *testing.T) {
	owner, tripID := uuid.New(), uuid.New()
	batches, signer := &batchRepositoryFake{}, &storeFake{}
	h := uploadTestHandler(owner, tripID, batches, signer)

	response := tripRequest(h, "POST", "/api/trips/"+tripID.String()+"/batches",
		`{"files":[{"client_file_id":"x","size_bytes":5,"content_type":"text/plain"}]}`, "Bearer valid")
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"accepted_files":[]`) {
		t.Fatalf("accepted_files must be an empty array: %s", response.Body.String())
	}
	if signer.calls != 0 || batches.calls != 1 || len(batches.files) != 0 {
		t.Fatalf("signer calls=%d batch calls=%d files=%d", signer.calls, batches.calls, len(batches.files))
	}
}

func TestRegisterBatchRejectsMalformedRequests(t *testing.T) {
	owner, tripID := uuid.New(), uuid.New()
	file := func(fields string) string { return `{"files":[{` + fields + `}]}` }
	valid := `"client_file_id":"a","size_bytes":1,"content_type":"image/jpeg"`
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf(`{"client_file_id":"f%d","size_bytes":1,"content_type":"image/jpeg"}`, i)
	}

	for name, body := range map[string]string{
		"not json":            `{`,
		"trailing data":       file(valid) + `{}`,
		"null body":           `null`,
		"unknown top field":   `{"files":[],"extra":1}`,
		"missing files":       `{}`,
		"null files":          `{"files":null}`,
		"files not array":     `{"files":{}}`,
		"empty files":         `{"files":[]}`,
		"too many files":      `{"files":[` + strings.Join(tooMany, ",") + `]}`,
		"null file":           `{"files":[null]}`,
		"unknown file field":  file(valid + `,"name":"x.jpg"`),
		"missing id":          file(`"size_bytes":1,"content_type":"image/jpeg"`),
		"empty id":            file(`"client_file_id":"","size_bytes":1,"content_type":"image/jpeg"`),
		"long id":             file(`"client_file_id":"` + strings.Repeat("é", 101) + `","size_bytes":1,"content_type":"image/jpeg"`),
		"numeric id":          file(`"client_file_id":1,"size_bytes":1,"content_type":"image/jpeg"`),
		"null size":           file(`"client_file_id":"a","size_bytes":null,"content_type":"image/jpeg"`),
		"string size":         file(`"client_file_id":"a","size_bytes":"12","content_type":"image/jpeg"`),
		"fractional size":     file(`"client_file_id":"a","size_bytes":1.5,"content_type":"image/jpeg"`),
		"zero size":           file(`"client_file_id":"a","size_bytes":0,"content_type":"image/jpeg"`),
		"missing type":        file(`"client_file_id":"a","size_bytes":1`),
		"duplicate client id": `{"files":[{` + valid + `},{` + valid + `}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			batches, signer := &batchRepositoryFake{}, &storeFake{}
			h := uploadTestHandler(owner, tripID, batches, signer)
			response := tripRequest(h, "POST", "/api/trips/"+tripID.String()+"/batches", body, "Bearer valid")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if signer.calls != 0 || batches.calls != 0 {
				t.Fatalf("signer calls=%d batch calls=%d", signer.calls, batches.calls)
			}
		})
	}
}

func TestRegisterBatchHidesOtherUsersTrips(t *testing.T) {
	owner, ownedTrip := uuid.New(), uuid.New()
	body := `{"files":[{"client_file_id":"a","size_bytes":1,"content_type":"image/jpeg"}]}`
	for name, path := range map[string]string{
		"other user's trip": "/api/trips/" + uuid.NewString() + "/batches",
		"invalid trip id":   "/api/trips/not-a-uuid/batches",
	} {
		t.Run(name, func(t *testing.T) {
			batches, signer := &batchRepositoryFake{}, &storeFake{}
			response := tripRequest(uploadTestHandler(owner, ownedTrip, batches, signer), "POST", path, body, "Bearer valid")
			if response.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if signer.calls != 0 || batches.calls != 0 {
				t.Fatalf("signer calls=%d batch calls=%d", signer.calls, batches.calls)
			}
		})
	}
}

func TestRegisterBatchRequiresAuthentication(t *testing.T) {
	owner, tripID := uuid.New(), uuid.New()
	batches, signer := &batchRepositoryFake{}, &storeFake{}
	h := uploadTestHandler(owner, tripID, batches, signer)
	body := `{"files":[{"client_file_id":"a","size_bytes":1,"content_type":"image/jpeg"}]}`
	for _, authorization := range []string{"", "Bearer invalid"} {
		response := tripRequest(h, "POST", "/api/trips/"+tripID.String()+"/batches", body, authorization)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q: status=%d", authorization, response.Code)
		}
	}
	if signer.calls != 0 || batches.calls != 0 {
		t.Fatalf("signer calls=%d batch calls=%d", signer.calls, batches.calls)
	}
}

func TestRegisterBatchStorageFailures(t *testing.T) {
	owner, tripID := uuid.New(), uuid.New()
	body := `{"files":[{"client_file_id":"a","size_bytes":1,"content_type":"image/jpeg"}]}`

	t.Run("signing fails before anything is stored", func(t *testing.T) {
		batches, signer := &batchRepositoryFake{}, &storeFake{err: errors.New("iam denied")}
		response := tripRequest(uploadTestHandler(owner, tripID, batches, signer), "POST", "/api/trips/"+tripID.String()+"/batches", body, "Bearer valid")
		if response.Code != http.StatusInternalServerError || batches.calls != 0 {
			t.Fatalf("status=%d batch calls=%d", response.Code, batches.calls)
		}
		if strings.Contains(response.Body.String(), "iam denied") {
			t.Fatal("internal error leaked to client")
		}
	})
	t.Run("trip disappears before insert", func(t *testing.T) {
		batches, signer := &batchRepositoryFake{err: uploads.ErrTripNotFound}, &storeFake{}
		response := tripRequest(uploadTestHandler(owner, tripID, batches, signer), "POST", "/api/trips/"+tripID.String()+"/batches", body, "Bearer valid")
		if response.Code != http.StatusNotFound {
			t.Fatalf("status=%d", response.Code)
		}
	})
	t.Run("database error", func(t *testing.T) {
		batches, signer := &batchRepositoryFake{err: errors.New("connection reset")}, &storeFake{}
		response := tripRequest(uploadTestHandler(owner, tripID, batches, signer), "POST", "/api/trips/"+tripID.String()+"/batches", body, "Bearer valid")
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", response.Code)
		}
	})
}

func TestUploadRouteAbsentWithoutBucket(t *testing.T) {
	h := persistentTestHandler(t, uuid.New(), tripRepositoryFake{})
	response := tripRequest(h, "POST", "/api/trips/"+uuid.NewString()+"/batches", `{"files":[]}`, "Bearer valid")
	if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d; upload route must not exist without a bucket", response.Code)
	}
}
