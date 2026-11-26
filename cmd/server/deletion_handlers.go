package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/TravelMapMory/mapmory-backend/internal/appuser"
	"github.com/TravelMapMory/mapmory-backend/internal/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/erasure"
	"github.com/TravelMapMory/mapmory-backend/internal/photostorage"
	"github.com/google/uuid"
)

const (
	// objectDeleteParallelism bounds concurrent storage deletions.
	objectDeleteParallelism = 8
	// objectDeleteTimeout bounds removing one deletion's objects.
	objectDeleteTimeout = 2 * time.Minute
	// photoObjectTimeout bounds removing one photo's two objects, retries
	// included, while its row is locked.
	photoObjectTimeout = 30 * time.Second
	// uploadQuietAfter is how long after registration a pending photo can no
	// longer receive its file: the signed upload URL's lifetime plus a margin
	// for an upload that started just before the URL expired.
	uploadQuietAfter = photostorage.UploadTTL + 5*time.Minute
)

// photoObjectRetryDelays are the pauses before retrying a failed object
// removal; tests shorten them.
var photoObjectRetryDelays = []time.Duration{250 * time.Millisecond, time.Second}

// objectDeleter is the part of photo storage that deletion needs.
type objectDeleter interface {
	Delete(ctx context.Context, objectName string) error
}

// deletionHandlers serve DELETE /api/photos/{photo_id}, DELETE
// /api/trips/{trip_id} and DELETE /api/me (design doc 2.2 use case 6, 3.4, 3.6).
type deletionHandlers struct {
	repo erasure.Repository
	// store is nil when photo storage is not configured; there are then no
	// objects to remove.
	store objectDeleter
	// now is the clock for deciding whether a photo is still uploading;
	// nil means time.Now.
	now func() time.Time
}

func (h *deletionHandlers) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// removePhotoObjects deletes one photo's objects before its row goes, retrying
// each a few times. Any object still present after that fails the whole
// deletion and leaves the row in place for the owner to retry; objects
// removed before the failure stay removed (retryable, not atomic). A photo is
// never reported deleted while one of its images remains in storage.
func (h *deletionHandlers) removePhotoObjects(ctx context.Context, objects []string) error {
	if h.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, photoObjectTimeout)
	defer cancel()
	for _, name := range objects {
		err := h.store.Delete(ctx, name)
		for _, delay := range photoObjectRetryDelays {
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			case <-time.After(delay):
			}
			err = h.store.Delete(ctx, name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *deletionHandlers) deletePhoto(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	photoID, err := uuid.Parse(r.PathValue("photo_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "photo not found")
		return
	}
	now := h.clock()
	quiet := erasure.PhotoQuiet{
		UploadsBefore:    now.Add(-uploadQuietAfter),
		ProcessingBefore: now.Add(-staleProcessingAfter),
	}
	// A client that goes away must not abandon a deletion half-way.
	err = h.repo.DeletePhoto(context.WithoutCancel(r.Context()), owner, photoID, quiet, h.removePhotoObjects)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, erasure.ErrPhotoNotFound):
		writeError(w, http.StatusNotFound, "not_found", "photo not found")
	case errors.Is(err, erasure.ErrPhotoBusy):
		writeError(w, http.StatusConflict, "photo_busy", "the photo is still uploading or processing; try again in a few minutes")
	case errors.Is(err, erasure.ErrCleanup):
		// Object names stay out of the log, like for trip deletion.
		log.Printf("photo deletion: storage cleanup failed; the photo was kept for a retry")
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "the photo could not be deleted right now; try again")
	default:
		tripStorageError(w, err)
	}
}

// removeObjects deletes stored images after the database commit. A failure
// leaves an unreachable object behind but never restores access: the rows that
// authorized it are gone. Object names are logged only as a count.
func (h *deletionHandlers) removeObjects(ctx context.Context, objects []string) {
	if h.store == nil || len(objects) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), objectDeleteTimeout)
	defer cancel()
	slots := make(chan struct{}, objectDeleteParallelism)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for _, name := range objects {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			if err := h.store.Delete(ctx, name); err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if failed > 0 {
		log.Printf("deletion left %d of %d storage objects behind", failed, len(objects))
	}
}

func (h *deletionHandlers) deleteTrip(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	tripID, err := uuid.Parse(r.PathValue("trip_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	objects, err := h.repo.DeleteTrip(context.WithoutCancel(r.Context()), owner, tripID)
	if errors.Is(err, erasure.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	if err != nil {
		tripStorageError(w, err)
		return
	}
	h.removeObjects(r.Context(), objects)
	w.WriteHeader(http.StatusNoContent)
}

func (h *deletionHandlers) deleteAccount(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	objects, err := h.repo.DeleteAccount(context.WithoutCancel(r.Context()), owner)
	if err != nil {
		tripStorageError(w, err)
		return
	}
	h.removeObjects(r.Context(), objects)
	w.WriteHeader(http.StatusNoContent)
}

// withDeletion adds the protected deletion routes in front of next.
func withDeletion(next http.Handler, verifier auth.Verifier, resolver appuser.Resolver, h *deletionHandlers) http.Handler {
	protected := func(handler http.HandlerFunc) http.Handler {
		return auth.RequireIdentity(verifier, appuser.RequireIdentity(resolver, handler))
	}
	mux := http.NewServeMux()
	mux.Handle("DELETE /api/photos/{photo_id}", protected(h.deletePhoto))
	mux.Handle("DELETE /api/trips/{trip_id}", protected(h.deleteTrip))
	mux.Handle("DELETE /api/me", protected(h.deleteAccount))
	mux.Handle("/", next)
	return mux
}
