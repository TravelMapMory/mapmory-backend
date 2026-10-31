package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/google/uuid"
)

func TestPersistentTripRoutesCannotFallThroughToPrototype(t *testing.T) {
	owner := uuid.New()
	verifierCalls, resolverCalls, repoCalls := 0, 0, 0
	verifier := endpointVerifierFunc(func(context.Context, string) (*firebaseauth.Token, error) {
		verifierCalls++
		return &firebaseauth.Token{UID: "user-a"}, nil
	})
	resolver := endpointResolverFunc(func(context.Context, string) (uuid.UUID, error) { resolverCalls++; return owner, nil })
	repo := tripRepositoryFake{list: func(context.Context, uuid.UUID) ([]trips.Trip, error) { repoCalls++; return nil, nil }}
	h := newHandlerWithPersistentTrips(verifier, resolver, repo)
	cases := []struct {
		method, path string
		status       int
	}{
		{"GET", "/api/trips/", http.StatusNotFound},
		{"POST", "/api/trips/", http.StatusNotFound},
		{"PUT", "/api/trips", http.StatusMethodNotAllowed},
		{"DELETE", "/api/trips/" + uuid.NewString(), http.StatusMethodNotAllowed},
		{"POST", "/api/trips/" + uuid.NewString(), http.StatusMethodNotAllowed},
		{"GET", "/api/trips/" + uuid.NewString() + "/extra", http.StatusNotFound},
		{"GET", "/api/unknown", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			response := tripRequest(h, tc.method, tc.path, `{"title":"unexpected"}`, "")
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
	if verifierCalls != 0 || resolverCalls != 0 || repoCalls != 0 {
		t.Fatalf("unexpected calls: verifier=%d resolver=%d repository=%d", verifierCalls, resolverCalls, repoCalls)
	}
}

func TestPersistentTripMiddlewareRejectsBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		name, header                       string
		verifierError                      bool
		resolverID                         uuid.UUID
		resolverError                      bool
		status                             int
		verificationCalls, resolutionCalls int
	}{
		{name: "missing header", status: 401},
		{name: "malformed header", header: "Basic token", status: 401},
		{name: "extra bearer tokens", header: "Bearer valid extra", status: 401},
		{name: "invalid token", header: "Bearer invalid", verifierError: true, status: 401, verificationCalls: 1},
		{name: "resolver unavailable", header: "Bearer valid", resolverError: true, status: 503, verificationCalls: 1, resolutionCalls: 1},
		{name: "resolver returned nil uuid", header: "Bearer valid", status: 503, verificationCalls: 1, resolutionCalls: 1},
		{name: "resolver returned uuid and error", header: "Bearer valid", resolverID: uuid.New(), resolverError: true, status: 503, verificationCalls: 1, resolutionCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verificationCalls, resolutionCalls := 0, 0
			verifier := endpointVerifierFunc(func(_ context.Context, token string) (*firebaseauth.Token, error) {
				verificationCalls++
				if tc.verifierError {
					return nil, errors.New("secret token failure")
				}
				return &firebaseauth.Token{UID: "firebase-user"}, nil
			})
			resolver := endpointResolverFunc(func(_ context.Context, uid string) (uuid.UUID, error) {
				resolutionCalls++
				if uid != "firebase-user" {
					t.Fatalf("unexpected subject %q", uid)
				}
				if tc.resolverError {
					return tc.resolverID, errors.New("secret connection failure")
				}
				return tc.resolverID, nil
			})
			h := newHandlerWithPersistentTrips(verifier, resolver, tripRepositoryFake{})
			for _, operation := range []struct{ method, path, body string }{
				{"GET", "/api/trips", ""},
				{"POST", "/api/trips", `{"title":"Japan"}`},
				{"GET", "/api/trips/" + uuid.NewString(), ""},
			} {
				rec := tripRequest(h, operation.method, operation.path, operation.body, tc.header)
				if rec.Code != tc.status {
					t.Fatalf("%s %s: status=%d want=%d body=%s", operation.method, operation.path, rec.Code, tc.status, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), "secret") {
					t.Fatalf("internal error leaked: %s", rec.Body.String())
				}
				if tc.status == 401 && rec.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Fatal("missing Bearer challenge")
				}
			}
			if verificationCalls != tc.verificationCalls*3 || resolutionCalls != tc.resolutionCalls*3 {
				t.Fatalf("calls: verifier=%d want=%d, resolver=%d want=%d", verificationCalls, tc.verificationCalls*3, resolutionCalls, tc.resolutionCalls*3)
			}
		})
	}
}

func TestPersistentTripRepositoryErrorsAreSafeForAllOperations(t *testing.T) {
	owner := uuid.New()
	failure := errors.New("secret database details")
	repo := tripRepositoryFake{
		create: func(context.Context, uuid.UUID, string, string) (trips.Trip, error) { return trips.Trip{}, failure },
		list:   func(context.Context, uuid.UUID) ([]trips.Trip, error) { return nil, failure },
		get:    func(context.Context, uuid.UUID, uuid.UUID) (trips.Trip, error) { return trips.Trip{}, failure },
	}
	h := persistentTestHandler(t, owner, repo)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/trips", ""},
		{"POST", "/api/trips", `{"title":"Japan"}`},
		{"GET", "/api/trips/" + uuid.NewString(), ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := tripRequest(h, tc.method, tc.path, tc.body, "Bearer valid")
			if rec.Code != 500 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("content type=%s", rec.Header().Get("Content-Type"))
			}
			var response errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Code != "internal_error" || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("unsafe response: %s", rec.Body.String())
			}
		})
	}
}

func TestPersistentTripInvalidIdentifiersDoNotReachRepository(t *testing.T) {
	h := persistentTestHandler(t, uuid.New(), tripRepositoryFake{})
	for _, id := range []string{"not-a-uuid", "123"} {
		rec := tripRequest(h, "GET", "/api/trips/"+id, "", "Bearer valid")
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
			t.Fatalf("id=%q status=%d body=%s", id, rec.Code, rec.Body.String())
		}
	}
}

func TestPersistentTripHealthAndIdentityRoutesStillAvailable(t *testing.T) {
	h := persistentTestHandler(t, uuid.New(), tripRepositoryFake{})
	for _, tc := range []struct {
		path, header string
		status       int
	}{
		{"/healthz", "", 200},
		{"/api/auth/me", "Bearer valid", 200},
		{"/api/users/me", "Bearer valid", 200},
		{"/api/users/me", "", 401},
	} {
		rec := tripRequest(h, "GET", tc.path, "", tc.header)
		if rec.Code != tc.status {
			t.Fatalf("%s: status=%d want=%d body=%s", tc.path, rec.Code, tc.status, rec.Body.String())
		}
	}
}
