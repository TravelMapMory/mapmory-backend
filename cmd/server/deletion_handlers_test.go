package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/erasure"
	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/google/uuid"
)

// erasureFake deletes one known trip, or one known photo, of one owner.
type erasureFake struct {
	owner, tripID uuid.UUID
	objects       []string
	err           error
	trips         []uuid.UUID
	accounts      []uuid.UUID

	// photoID is the one photo, owned by photoOwner (owner when nil), with
	// photoObjects in storage; photoBusy makes it still uploading.
	// deletedPhotos and quiet record calls.
	photoID       uuid.UUID
	photoOwner    uuid.UUID
	photoObjects  []string
	photoBusy     bool
	deletedPhotos []uuid.UUID
	quiet         erasure.PhotoQuiet
}

func (f *erasureFake) DeletePhoto(ctx context.Context, owner, photo uuid.UUID, quiet erasure.PhotoQuiet, remove erasure.RemoveObjects) error {
	f.quiet = quiet
	if f.err != nil {
		return f.err
	}
	photoOwner := f.photoOwner
	if photoOwner == uuid.Nil {
		photoOwner = f.owner
	}
	if owner != photoOwner || photo != f.photoID || f.photoID == uuid.Nil {
		return erasure.ErrPhotoNotFound
	}
	if f.photoBusy {
		return erasure.ErrPhotoBusy
	}
	if err := remove(ctx, f.photoObjects); err != nil {
		return fmt.Errorf("%w: %w", erasure.ErrCleanup, err)
	}
	f.deletedPhotos = append(f.deletedPhotos, photo)
	f.photoID = uuid.Nil
	return nil
}

// flakyStore fails the first failures deletions of every object.
type flakyStore struct {
	mu       sync.Mutex
	failures int
	attempts map[string]int
	deleted  []string
}

func (s *flakyStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts == nil {
		s.attempts = make(map[string]int)
	}
	s.attempts[name]++
	if s.attempts[name] <= s.failures {
		return errors.New("storage unavailable: " + name)
	}
	s.deleted = append(s.deleted, name)
	return nil
}

func noRetryDelays(t *testing.T) {
	t.Helper()
	saved := photoObjectRetryDelays
	photoObjectRetryDelays = []time.Duration{0, 0}
	t.Cleanup(func() { photoObjectRetryDelays = saved })
}

func newPhotoDeletionRepo() *erasureFake {
	photo, trip := uuid.New(), uuid.New()
	return &erasureFake{owner: uuid.New(), photoID: photo, photoObjects: []string{
		"originals/" + trip.String() + "/" + photo.String(),
		"display/" + trip.String() + "/" + photo.String() + ".jpg",
	}}
}

// deletionTestNow is the handlers' clock in these tests.
var deletionTestNow = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func (f *erasureFake) DeleteTrip(_ context.Context, owner, trip uuid.UUID) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	if owner != f.owner || trip != f.tripID {
		return nil, erasure.ErrNotFound
	}
	f.trips = append(f.trips, trip)
	return f.objects, nil
}

func (f *erasureFake) DeleteAccount(_ context.Context, owner uuid.UUID) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.accounts = append(f.accounts, owner)
	return f.objects, nil
}

func newDeletionTestHandler(t *testing.T, repo *erasureFake, store objectDeleter) http.Handler {
	t.Helper()
	verifier := endpointVerifierFunc(func(_ context.Context, token string) (*firebaseauth.Token, error) {
		if token != "valid" {
			return nil, errors.New("invalid token")
		}
		return &firebaseauth.Token{UID: "subject-a"}, nil
	})
	resolver := endpointResolverFunc(func(context.Context, string) (uuid.UUID, error) { return repo.owner, nil })
	tripRepo := tripRepositoryFake{list: func(context.Context, uuid.UUID) ([]trips.Trip, error) { return []trips.Trip{}, nil }}
	inner := newPersistentHandler(verifier, resolver, tripRepo, nil)
	return withDeletion(inner, verifier, resolver, &deletionHandlers{
		repo: repo, store: store, now: func() time.Time { return deletionTestNow },
	})
}

func TestDeleteTripRemovesRowsThenObjects(t *testing.T) {
	store := &storeFake{}
	repo := &erasureFake{owner: uuid.New(), tripID: uuid.New(), objects: []string{"originals/a", "display/a.jpg", "originals/b"}}
	handler := newDeletionTestHandler(t, repo, store)

	response := tripRequest(handler, "DELETE", "/api/trips/"+repo.tripID.String(), "", "Bearer valid")
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	sort.Strings(store.deleted)
	if len(repo.trips) != 1 || strings.Join(store.deleted, ",") != "display/a.jpg,originals/a,originals/b" {
		t.Fatalf("trips=%v deleted objects=%v", repo.trips, store.deleted)
	}
}

func TestDeleteTripAccess(t *testing.T) {
	store := &storeFake{}
	repo := &erasureFake{owner: uuid.New(), tripID: uuid.New(), objects: []string{"originals/a"}}
	handler := newDeletionTestHandler(t, repo, store)

	if response := tripRequest(handler, "DELETE", "/api/trips/"+repo.tripID.String(), "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d", response.Code)
	}
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		if response := tripRequest(handler, "DELETE", "/api/trips/"+id, "", "Bearer valid"); response.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d", id, response.Code)
		}
	}
	if len(repo.trips) != 0 || len(store.deleted) != 0 {
		t.Fatal("a refused request deleted something")
	}
	// The other trip routes still work behind the deletion wrapper.
	if response := tripRequest(handler, "GET", "/api/trips", "", "Bearer valid"); response.Code != http.StatusOK {
		t.Fatalf("GET /api/trips behind the wrapper: status=%d", response.Code)
	}
}

func TestDeleteAccount(t *testing.T) {
	store := &storeFake{}
	repo := &erasureFake{owner: uuid.New(), objects: []string{"originals/x", "display/x.jpg"}}
	handler := newDeletionTestHandler(t, repo, store)

	if response := tripRequest(handler, "DELETE", "/api/me", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d", response.Code)
	}
	response := tripRequest(handler, "DELETE", "/api/me", "", "Bearer valid")
	if response.Code != http.StatusNoContent || len(repo.accounts) != 1 || repo.accounts[0] != repo.owner || len(store.deleted) != 2 {
		t.Fatalf("status=%d accounts=%v deleted=%v", response.Code, repo.accounts, store.deleted)
	}
}

func TestDeletionWithoutStorageAndOnFailure(t *testing.T) {
	repo := &erasureFake{owner: uuid.New(), tripID: uuid.New(), objects: []string{"originals/a"}}
	// No photo storage configured: rows are still deleted.
	if response := tripRequest(newDeletionTestHandler(t, repo, nil), "DELETE", "/api/trips/"+repo.tripID.String(), "", "Bearer valid"); response.Code != http.StatusNoContent {
		t.Fatalf("without storage: status=%d", response.Code)
	}
	store := &storeFake{}
	failing := &erasureFake{owner: uuid.New(), err: errors.New("secret sql detail")}
	response := tripRequest(newDeletionTestHandler(t, failing, store), "DELETE", "/api/me", "", "Bearer valid")
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret") || len(store.deleted) != 0 {
		t.Fatalf("failure: status=%d body=%s deleted=%v", response.Code, response.Body.String(), store.deleted)
	}
}

func TestDeletePhotoRemovesObjectsThenRow(t *testing.T) {
	store := &storeFake{}
	repo := newPhotoDeletionRepo()
	photo := repo.photoID
	handler := newDeletionTestHandler(t, repo, store)

	response := tripRequest(handler, "DELETE", "/api/photos/"+photo.String(), "", "Bearer valid")
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repo.deletedPhotos) != 1 || repo.deletedPhotos[0] != photo {
		t.Fatalf("deleted photos = %v", repo.deletedPhotos)
	}
	if strings.Join(store.deleted, ",") != strings.Join(repo.photoObjects, ",") {
		t.Fatalf("deleted objects = %v, want %v", store.deleted, repo.photoObjects)
	}
	// An upload URL lives 15 minutes (plus a 5-minute margin); processing is
	// stale after 10 minutes.
	if !repo.quiet.UploadsBefore.Equal(deletionTestNow.Add(-20*time.Minute)) ||
		!repo.quiet.ProcessingBefore.Equal(deletionTestNow.Add(-10*time.Minute)) {
		t.Fatalf("quiet = %+v", repo.quiet)
	}
	// Deleting again: the photo is gone.
	if again := tripRequest(handler, "DELETE", "/api/photos/"+photo.String(), "", "Bearer valid"); again.Code != http.StatusNotFound {
		t.Fatalf("repeat: status=%d", again.Code)
	}
}

func TestDeletePhotoAccess(t *testing.T) {
	store := &storeFake{}
	repo := newPhotoDeletionRepo()
	handler := newDeletionTestHandler(t, repo, store)

	if response := tripRequest(handler, "DELETE", "/api/photos/"+repo.photoID.String(), "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d", response.Code)
	}
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		response := tripRequest(handler, "DELETE", "/api/photos/"+id, "", "Bearer valid")
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"not_found"`) {
			t.Fatalf("%s: status=%d body=%s", id, response.Code, response.Body.String())
		}
	}
	// Another owner's photo answers exactly like a photo that does not exist.
	foreign := newPhotoDeletionRepo()
	foreign.photoOwner = uuid.New()
	response := tripRequest(newDeletionTestHandler(t, foreign, store), "DELETE", "/api/photos/"+foreign.photoID.String(), "", "Bearer valid")
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"not_found"`) {
		t.Fatalf("foreign: status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repo.deletedPhotos) != 0 || len(foreign.deletedPhotos) != 0 || len(store.deleted) != 0 {
		t.Fatal("a refused request deleted something")
	}
	// The other photo routes still reach the inner handler.
	if response := tripRequest(handler, "GET", "/api/trips", "", "Bearer valid"); response.Code != http.StatusOK {
		t.Fatalf("GET /api/trips behind the wrapper: status=%d", response.Code)
	}
}

func TestDeletePhotoWhileUploading(t *testing.T) {
	store := &storeFake{}
	repo := newPhotoDeletionRepo()
	repo.photoBusy = true
	response := tripRequest(newDeletionTestHandler(t, repo, store), "DELETE", "/api/photos/"+repo.photoID.String(), "", "Bearer valid")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"photo_busy"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repo.deletedPhotos) != 0 || len(store.deleted) != 0 {
		t.Fatal("a busy photo was deleted")
	}
}

func TestDeletePhotoRetriesStorageAndKeepsThePhotoOnFailure(t *testing.T) {
	noRetryDelays(t)

	// A transient failure is retried and the deletion succeeds.
	transient := &flakyStore{failures: 1}
	repo := newPhotoDeletionRepo()
	response := tripRequest(newDeletionTestHandler(t, repo, transient), "DELETE", "/api/photos/"+repo.photoID.String(), "", "Bearer valid")
	if response.Code != http.StatusNoContent || len(transient.deleted) != 2 || len(repo.deletedPhotos) != 1 {
		t.Fatalf("transient: status=%d deleted=%v photos=%v", response.Code, transient.deleted, repo.deletedPhotos)
	}

	// Storage stays down: 503, the photo is kept, nothing internal leaks.
	down := &flakyStore{failures: 100}
	repo = newPhotoDeletionRepo()
	handler := newDeletionTestHandler(t, repo, down)
	response = tripRequest(handler, "DELETE", "/api/photos/"+repo.photoID.String(), "", "Bearer valid")
	body := response.Body.String()
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(body, `"storage_unavailable"`) ||
		strings.Contains(body, "originals/") || strings.Contains(body, "storage unavailable:") {
		t.Fatalf("down: status=%d body=%s", response.Code, body)
	}
	if len(repo.deletedPhotos) != 0 || down.attempts[repo.photoObjects[0]] != 1+len(photoObjectRetryDelays) {
		t.Fatalf("down: photos=%v attempts=%v", repo.deletedPhotos, down.attempts)
	}

	// Storage is back: repeating the request finishes the deletion.
	down.failures = 0
	response = tripRequest(handler, "DELETE", "/api/photos/"+repo.photoID.String(), "", "Bearer valid")
	if response.Code != http.StatusNoContent || len(repo.deletedPhotos) != 1 {
		t.Fatalf("retry: status=%d photos=%v", response.Code, repo.deletedPhotos)
	}
}

func TestDeletePhotoDatabaseFailure(t *testing.T) {
	store := &storeFake{}
	repo := newPhotoDeletionRepo()
	repo.err = errors.New("secret sql detail")
	response := tripRequest(newDeletionTestHandler(t, repo, store), "DELETE", "/api/photos/"+repo.photoID.String(), "", "Bearer valid")
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret") || len(store.deleted) != 0 {
		t.Fatalf("status=%d body=%s deleted=%v", response.Code, response.Body.String(), store.deleted)
	}
}
