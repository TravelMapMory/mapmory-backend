package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/erasure"
	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/google/uuid"
)

// erasureFake deletes one known trip of one owner.
type erasureFake struct {
	owner, tripID uuid.UUID
	objects       []string
	err           error
	trips         []uuid.UUID
	accounts      []uuid.UUID
}

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
	return withDeletion(inner, verifier, resolver, &deletionHandlers{repo: repo, store: store})
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
