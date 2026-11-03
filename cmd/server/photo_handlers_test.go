package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/photos"
	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/TravelMapMory/mapmory-backend/internal/uploads"
	"github.com/google/uuid"
)

// photoRepositoryFake mirrors the PostgreSQL state transitions in memory.
type photoRepositoryFake struct {
	mu        sync.Mutex
	owner     uuid.UUID
	tripID    uuid.UUID
	photos    map[uuid.UUID]*photos.Photo
	finishErr error
}

func newPhotoRepositoryFake(owner, tripID uuid.UUID) *photoRepositoryFake {
	return &photoRepositoryFake{owner: owner, tripID: tripID, photos: make(map[uuid.UUID]*photos.Photo)}
}

// add registers a pending photo and returns it.
func (f *photoRepositoryFake) add(batchID uuid.UUID, clientFileID string, size int64) photos.Photo {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.New()
	photo := &photos.Photo{
		ID: id, TripID: f.tripID, BatchID: batchID, ClientFileID: clientFileID, State: photos.StatePending,
		ObjectName: uploads.ObjectName(f.tripID, id), ContentType: "image/jpeg", SizeBytes: size, CreatedAt: time.Now(),
	}
	f.photos[id] = photo
	return *photo
}

func (f *photoRepositoryFake) get(id uuid.UUID) photos.Photo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.photos[id]
}

func (f *photoRepositoryFake) GetByClientFile(_ context.Context, owner, batchID uuid.UUID, clientFileID string) (photos.Photo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, photo := range f.photos {
		if owner == f.owner && photo.BatchID == batchID && photo.ClientFileID == clientFileID {
			return *photo, nil
		}
	}
	return photos.Photo{}, photos.ErrNotFound
}

func (f *photoRepositoryFake) Claim(_ context.Context, id uuid.UUID, staleBefore time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	photo := f.photos[id]
	stale := photo.State == photos.StateProcessing && photo.ProcessingStarted.Before(staleBefore)
	if photo.State != photos.StatePending && !stale {
		return false, nil
	}
	now := time.Now()
	photo.State, photo.ProcessingStarted = photos.StateProcessing, &now
	return true, nil
}

func (f *photoRepositoryFake) Release(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if photo := f.photos[id]; photo.State == photos.StateProcessing {
		photo.State, photo.ProcessingStarted = photos.StatePending, nil
	}
	return nil
}

func (f *photoRepositoryFake) Fail(_ context.Context, id uuid.UUID, uploadedAt time.Time, code string) (photos.Photo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	photo := f.photos[id]
	photo.State, photo.ErrorCode, photo.UploadedAt, photo.ProcessingStarted = photos.StateFailed, &code, &uploadedAt, nil
	return *photo, nil
}

func (f *photoRepositoryFake) Finish(_ context.Context, id uuid.UUID, result photos.Processed) (photos.Photo, error) {
	if f.finishErr != nil {
		return photos.Photo{}, f.finishErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	photo := f.photos[id]
	photo.State = photos.StateNeedsLocation
	if result.ExifLat != nil {
		photo.State = photos.StateReady
	}
	photo.UploadedAt, photo.ProcessingStarted = &result.UploadedAt, nil
	photo.CaptureTimeLocal, photo.CaptureUTCOffset = result.CaptureTimeLocal, result.CaptureUTCOffset
	photo.ExifLat, photo.ExifLng, photo.Lat, photo.Lng = result.ExifLat, result.ExifLng, result.ExifLat, result.ExifLng
	photo.DisplayObjectName = &result.DisplayObjectName
	return *photo, nil
}

// get returns a photo by ID if the caller owns it
func (f *photoRepositoryFake) Get(_ context.Context, owner, id uuid.UUID) (photos.Photo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	photo, ok := f.photos[id]
	if owner != f.owner || !ok {
		return photos.Photo{}, photos.ErrNotFound
	}
	return *photo, nil
}

// capturesBefore reports whether a precedes b in the capture order of ListByTrip: capture time, then upload time, then ID; missing values last
func capturesBefore(a, b photos.Photo) bool {
	switch {
	case (a.CaptureTimeLocal == nil) != (b.CaptureTimeLocal == nil):
		return a.CaptureTimeLocal != nil
	case a.CaptureTimeLocal != nil && *a.CaptureTimeLocal != *b.CaptureTimeLocal:
		return *a.CaptureTimeLocal < *b.CaptureTimeLocal
	case (a.UploadedAt == nil) != (b.UploadedAt == nil):
		return a.UploadedAt != nil
	case a.UploadedAt != nil && !a.UploadedAt.Equal(*b.UploadedAt):
		return a.UploadedAt.Before(*b.UploadedAt)
	default:
		return a.ID.String() < b.ID.String()
	}
}

func (f *photoRepositoryFake) GetBatch(_ context.Context, owner, batchID uuid.UUID) (uuid.UUID, []photos.Photo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []photos.Photo
	for _, photo := range f.photos {
		if photo.BatchID == batchID {
			result = append(result, *photo)
		}
	}
	if owner != f.owner || len(result) == 0 {
		return uuid.Nil, nil, photos.ErrNotFound
	}
	return f.tripID, result, nil
}

func (f *photoRepositoryFake) ListByTrip(_ context.Context, owner, tripID uuid.UUID, after *photos.Cursor, limit int) ([]photos.Photo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if owner != f.owner || tripID != f.tripID {
		return nil, photos.ErrNotFound
	}
	all := make([]photos.Photo, 0, len(f.photos))
	for _, photo := range f.photos {
		all = append(all, *photo)
	}
	less := capturesBefore
	sort.Slice(all, func(i, j int) bool { return less(all[i], all[j]) })
	start := 0
	if after != nil {
		marker := photos.Photo{ID: after.ID, CaptureTimeLocal: after.CaptureTimeLocal, UploadedAt: after.UploadedAt}
		for start < len(all) && !less(marker, all[start]) {
			start++
		}
	}
	all = all[start:]
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

type photoTestEnv struct {
	owner, tripID, batchID uuid.UUID
	repo                   *photoRepositoryFake
	store                  *storeFake
	handler                http.Handler
}

func newPhotoTestEnv(t *testing.T) *photoTestEnv {
	t.Helper()
	env := &photoTestEnv{owner: uuid.New(), tripID: uuid.New(), batchID: uuid.New(), store: &storeFake{}}
	env.repo = newPhotoRepositoryFake(env.owner, env.tripID)
	verifier := endpointVerifierFunc(func(_ context.Context, token string) (*firebaseauth.Token, error) {
		if token != "valid" {
			return nil, errors.New("invalid token")
		}
		return &firebaseauth.Token{UID: "subject-a"}, nil
	})
	resolver := endpointResolverFunc(func(context.Context, string) (uuid.UUID, error) { return env.owner, nil })
	tripRepo := tripRepositoryFake{}
	env.handler = newPersistentHandler(verifier, resolver, tripRepo,
		newUploadHandlers(tripRepo, &batchRepositoryFake{}, env.repo, env.store))
	return env
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../internal/photometa/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// registerUploaded adds a pending photo whose original is already in the bucket.
func (env *photoTestEnv) registerUploaded(clientFileID string, data []byte) photos.Photo {
	photo := env.repo.add(env.batchID, clientFileID, int64(len(data)))
	env.store.put(photo.ObjectName, "image/jpeg", data)
	return photo
}

func (env *photoTestEnv) complete(clientFileID string) (*apiPhoto, int, string) {
	response := tripRequest(env.handler, "POST",
		"/api/batches/"+env.batchID.String()+"/files/"+clientFileID+"/complete", "", "Bearer valid")
	if response.Code != http.StatusOK {
		return nil, response.Code, response.Body.String()
	}
	var photo apiPhoto
	if err := json.Unmarshal(response.Body.Bytes(), &photo); err != nil {
		panic(err)
	}
	return &photo, response.Code, response.Body.String()
}

func TestCompleteUploadProcessesPhotoWithGPS(t *testing.T) {
	env := newPhotoTestEnv(t)
	photo := env.registerUploaded("a", readFixture(t, "gps.jpg"))

	got, status, body := env.complete("a")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if got.State != photos.StateReady || got.ErrorCode != nil {
		t.Fatalf("state=%s error=%v", got.State, got.ErrorCode)
	}
	if got.ExifLat == nil || *got.ExifLat < 60.18 || *got.ExifLat > 60.19 || got.Lat == nil || *got.Lat != *got.ExifLat {
		t.Fatalf("coordinates exif=%v lat=%v", got.ExifLat, got.Lat)
	}
	if got.CaptureTimeLocal == nil || *got.CaptureTimeLocal != "2025-07-14T09:30:00" ||
		got.CaptureUTCOffset == nil || *got.CaptureUTCOffset != "+03:00" {
		t.Fatalf("capture time=%v offset=%v", got.CaptureTimeLocal, got.CaptureUTCOffset)
	}
	displayName := uploads.DisplayObjectName(env.tripID, photo.ID)
	if got.DisplayImage == nil || !strings.Contains(got.DisplayImage.URL, displayName) {
		t.Fatalf("display image = %+v", got.DisplayImage)
	}
	display, ok := env.store.objects[displayName]
	if !ok || display.contentType != "image/jpeg" || len(display.data) == 0 {
		t.Fatalf("display object not written: %+v", display)
	}
	if strings.Contains(body, "originals/") {
		t.Fatalf("response exposes the original: %s", body)
	}

	// Repeating the call returns the stored photo without reprocessing.
	again, status, _ := env.complete("a")
	if status != http.StatusOK || again.ID != got.ID || again.State != got.State {
		t.Fatalf("repeat status=%d photo=%+v", status, again)
	}
	if env.store.reads != 1 || env.store.writes != 1 {
		t.Fatalf("reads=%d writes=%d; repeat must not reprocess", env.store.reads, env.store.writes)
	}
}

func TestCompleteUploadWithoutGPSNeedsLocation(t *testing.T) {
	env := newPhotoTestEnv(t)
	env.registerUploaded("a", readFixture(t, "no_exif.jpg"))

	got, status, body := env.complete("a")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if got.State != photos.StateNeedsLocation || got.DisplayImage == nil {
		t.Fatalf("state=%s display=%v", got.State, got.DisplayImage)
	}
	for _, field := range []string{`"exif_lat":null`, `"lat":null`, `"capture_time_local":null`, `"capture_utc_offset":null`} {
		if !strings.Contains(body, field) {
			t.Fatalf("missing %s in %s", field, body)
		}
	}
}

func TestCompleteUploadRecordsInvalidImage(t *testing.T) {
	env := newPhotoTestEnv(t)
	env.registerUploaded("a", []byte("this is not an image at all"))

	got, status, body := env.complete("a")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if got.State != photos.StateFailed || got.ErrorCode == nil || *got.ErrorCode != "unsupported_type" || got.DisplayImage != nil {
		t.Fatalf("photo = %s", body)
	}
	if _, status, _ := env.complete("a"); status != http.StatusOK || env.store.reads != 1 {
		t.Fatalf("repeat status=%d reads=%d; a failed photo is not reprocessed", status, env.store.reads)
	}
}

func TestCompleteUploadRejectsSizeMismatch(t *testing.T) {
	env := newPhotoTestEnv(t)
	photo := env.repo.add(env.batchID, "a", 999)
	env.store.put(photo.ObjectName, "image/jpeg", readFixture(t, "gps.jpg"))

	got, status, body := env.complete("a")
	if status != http.StatusOK || got.State != photos.StateFailed || *got.ErrorCode != "invalid_image" {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if env.store.reads != 0 {
		t.Fatal("a mismatched object must not be downloaded")
	}
}

func TestCompleteUploadMissingObject(t *testing.T) {
	env := newPhotoTestEnv(t)
	photo := env.repo.add(env.batchID, "a", 100)

	_, status, body := env.complete("a")
	if status != http.StatusConflict || !strings.Contains(body, "upload_missing") {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if state := env.repo.get(photo.ID).State; state != photos.StatePending {
		t.Fatalf("state=%s; a missing upload must stay retryable", state)
	}
}

func TestCompleteUploadInProgressAndStaleClaims(t *testing.T) {
	env := newPhotoTestEnv(t)
	photo := env.registerUploaded("a", readFixture(t, "gps.jpg"))
	recent := time.Now().Add(-time.Minute)
	env.repo.photos[photo.ID].State, env.repo.photos[photo.ID].ProcessingStarted = photos.StateProcessing, &recent

	_, status, body := env.complete("a")
	if status != http.StatusConflict || !strings.Contains(body, "processing_in_progress") {
		t.Fatalf("status=%d body=%s", status, body)
	}

	stale := time.Now().Add(-staleProcessingAfter - time.Minute)
	env.repo.photos[photo.ID].ProcessingStarted = &stale
	got, status, body := env.complete("a")
	if status != http.StatusOK || got.State != photos.StateReady {
		t.Fatalf("stale claim: status=%d body=%s", status, body)
	}
}

func TestCompleteUploadReleasesClaimOnServerError(t *testing.T) {
	env := newPhotoTestEnv(t)
	photo := env.registerUploaded("a", readFixture(t, "gps.jpg"))
	env.repo.finishErr = errors.New("database down")

	_, status, body := env.complete("a")
	if status != http.StatusInternalServerError || strings.Contains(body, "database down") {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if state := env.repo.get(photo.ID).State; state != photos.StatePending {
		t.Fatalf("state=%s; a server error must release the claim", state)
	}
}

func TestCompleteUploadHidesOtherUsersFiles(t *testing.T) {
	env := newPhotoTestEnv(t)
	env.registerUploaded("a", readFixture(t, "gps.jpg"))
	for _, path := range []string{
		"/api/batches/" + uuid.NewString() + "/files/a/complete",
		"/api/batches/" + env.batchID.String() + "/files/other/complete",
		"/api/batches/not-a-uuid/files/a/complete",
	} {
		if response := tripRequest(env.handler, "POST", path, "", "Bearer valid"); response.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d", path, response.Code)
		}
	}
	if response := tripRequest(env.handler, "POST", "/api/batches/"+env.batchID.String()+"/files/a/complete", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d", response.Code)
	}
	if env.store.reads != 0 {
		t.Fatal("unauthorized requests must not read objects")
	}
}

func TestGetBatchReturnsPhotoStates(t *testing.T) {
	env := newPhotoTestEnv(t)
	env.registerUploaded("done", readFixture(t, "gps.jpg"))
	env.repo.add(env.batchID, "waiting", 10)
	env.complete("done")

	response := tripRequest(env.handler, "GET", "/api/batches/"+env.batchID.String(), "", "Bearer valid")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var batch batchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, photo := range batch.Photos {
		states[photo.ClientFileID] = photo.State
	}
	if batch.TripID != env.tripID.String() || states["done"] != photos.StateReady || states["waiting"] != photos.StatePending {
		t.Fatalf("batch = %+v", batch)
	}
	if response := tripRequest(env.handler, "GET", "/api/batches/"+uuid.NewString(), "", "Bearer valid"); response.Code != http.StatusNotFound {
		t.Fatalf("unknown batch: status=%d", response.Code)
	}
}

func TestListTripPhotosPaginates(t *testing.T) {
	env := newPhotoTestEnv(t)
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	set := func(clientFileID, capture string, uploadedAfter time.Duration, uploaded bool) string {
		photo := env.repo.add(env.batchID, clientFileID, 10)
		stored := env.repo.photos[photo.ID]
		if capture != "" {
			stored.CaptureTimeLocal = &capture
		}
		if uploaded {
			at := base.Add(uploadedAfter)
			stored.UploadedAt = &at
		}
		return photo.ID.String()
	}
	// capture time decides, even against upload order.
	late := set("late", "2026-07-02T08:00:00", 0, true)
	early := set("early", "2026-07-01T08:00:00", 4*time.Minute, true)
	// equal capture times: the earlier upload first.
	tieSecond := set("tie-second", "2026-07-01T12:00:00", 3*time.Minute, true)
	tieFirst := set("tie-first", "2026-07-01T12:00:00", time.Minute, true)
	// undated photos come last, never-uploaded ones after them.
	undated := set("undated", "", 2*time.Minute, true)
	pending := set("pending", "", 0, false)
	want := []string{early, tieFirst, tieSecond, late, undated, pending}

	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		path := "/api/trips/" + env.tripID.String() + "/photos?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		response := tripRequest(env.handler, "GET", path, "", "Bearer valid")
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var page photoPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			got = append(got, item.ID)
		}
		if page.NextCursor == nil {
			break
		}
		if pages > 5 {
			t.Fatal("pagination does not terminate")
		}
		cursor = *page.NextCursor
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order\n got %v\nwant %v", got, want)
	}
}

func TestListTripPhotosValidation(t *testing.T) {
	env := newPhotoTestEnv(t)
	base := "/api/trips/" + env.tripID.String() + "/photos"

	response := tripRequest(env.handler, "GET", base, "", "Bearer valid")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[]`) ||
		!strings.Contains(response.Body.String(), `"next_cursor":null`) {
		t.Fatalf("empty trip: status=%d body=%s", response.Code, response.Body.String())
	}
	badCapture := "yesterday"
	badCursor := photos.Cursor{CaptureTimeLocal: &badCapture, ID: uuid.New()}.Encode()
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=x", "?cursor=not-a-cursor", "?cursor=" + badCursor} {
		if response := tripRequest(env.handler, "GET", base+query, "", "Bearer valid"); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d", query, response.Code)
		}
	}
	if response := tripRequest(env.handler, "GET", "/api/trips/"+uuid.NewString()+"/photos", "", "Bearer valid"); response.Code != http.StatusNotFound {
		t.Fatalf("other trip: status=%d", response.Code)
	}
	if response := tripRequest(env.handler, "GET", base, "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d", response.Code)
	}
}

// getPhoto returns one of the caller's photos (contract getPhoto)
func TestGetPhoto(t *testing.T) {
	env := newPhotoTestEnv(t)
	env.registerUploaded("done", readFixture(t, "gps.jpg"))
	completed, _, _ := env.complete("done")
	response := tripRequest(env.handler, "GET", "/api/photos/"+completed.ID, "", "Bearer valid")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var photo apiPhoto
	if err := json.Unmarshal(response.Body.Bytes(), &photo); err != nil {
		t.Fatal(err)
	}
	if photo.ID != completed.ID || photo.State != photos.StateReady || photo.DisplayImage == nil {
		t.Fatalf("photo = %+v", photo)
	}
	for _, path := range []string{"/api/photos/" + uuid.NewString(), "/api/photos/not-a-uuid"} {
		if response := tripRequest(env.handler, "GET", path, "", "Bearer valid"); response.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d", path, response.Code)
		}
	}
	if response := tripRequest(env.handler, "GET", "/api/photos/"+completed.ID, "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d", response.Code)
	}
	// photo now belongs to someone else
	env.repo.owner = uuid.New()
	if response := tripRequest(env.handler, "GET", "/api/photos/"+completed.ID, "", "Bearer valid"); response.Code != http.StatusNotFound {
		t.Fatalf("other owner: status=%d", response.Code)
	}
}

var _ trips.Repository = tripRepositoryFake{}
