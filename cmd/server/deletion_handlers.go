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
	"github.com/google/uuid"
)

const (
	// objectDeleteParallelism bounds concurrent storage deletions.
	objectDeleteParallelism = 8
	// objectDeleteTimeout bounds removing one deletion's objects.
	objectDeleteTimeout = 2 * time.Minute
)

// objectDeleter is the part of photo storage that deletion needs.
type objectDeleter interface {
	Delete(ctx context.Context, objectName string) error
}

// deletionHandlers serve DELETE /api/trips/{trip_id} and DELETE /api/me
// (design doc 2.2 use case 6, 3.4, 3.6).
type deletionHandlers struct {
	repo erasure.Repository
	// store is nil when photo storage is not configured; there are then no
	// objects to remove.
	store objectDeleter
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
	mux.Handle("DELETE /api/trips/{trip_id}", protected(h.deleteTrip))
	mux.Handle("DELETE /api/me", protected(h.deleteAccount))
	mux.Handle("/", next)
	return mux
}
