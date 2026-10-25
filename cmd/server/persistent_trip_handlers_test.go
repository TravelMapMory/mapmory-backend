package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/google/uuid"
)

// The fake verifies owner propagation without using PostgreSQL or Firebase.
type tripRepositoryFake struct {
	create func(context.Context, uuid.UUID, string, string) (trips.Trip, error)
	list   func(context.Context, uuid.UUID) ([]trips.Trip, error)
	get    func(context.Context, uuid.UUID, uuid.UUID) (trips.Trip, error)
}

func (f tripRepositoryFake) Create(ctx context.Context, owner uuid.UUID, title, notes string) (trips.Trip, error) {
	if f.create == nil {
		panic("unexpected Create")
	}
	return f.create(ctx, owner, title, notes)
}
func (f tripRepositoryFake) ListByOwner(ctx context.Context, owner uuid.UUID) ([]trips.Trip, error) {
	if f.list == nil {
		panic("unexpected ListByOwner")
	}
	return f.list(ctx, owner)
}
func (f tripRepositoryFake) GetByID(ctx context.Context, owner, id uuid.UUID) (trips.Trip, error) {
	if f.get == nil {
		panic("unexpected GetByID")
	}
	return f.get(ctx, owner, id)
}
func persistentTestHandler(t *testing.T, owner uuid.UUID, repo trips.Repository) http.Handler {
	t.Helper()
	verifier := endpointVerifierFunc(func(_ context.Context, token string) (*firebaseauth.Token, error) {
		if token != "valid" {
			return nil, errors.New("invalid token")
		}
		return &firebaseauth.Token{UID: "subject-a"}, nil
	})
	resolver := endpointResolverFunc(func(_ context.Context, subject string) (uuid.UUID, error) {
		if subject != "subject-a" {
			t.Errorf("subject = %q", subject)
		}
		return owner, nil
	})
	return newHandlerWithPersistentTrips(verifier, resolver, repo)
}
func tripRequest(h http.Handler, method, path, body, authorization string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestPersistentTripsRequireAuthentication(t *testing.T) {
	owner := uuid.New()
	h := persistentTestHandler(t, owner, tripRepositoryFake{})
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/trips", ""},
		{"POST", "/api/trips", `{"title":"Japan"}`},
		{"GET", "/api/trips/" + uuid.NewString(), ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			for _, header := range []string{"", "Bearer invalid"} {
				rec := tripRequest(h, tc.method, tc.path, tc.body, header)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
				}
				if rec.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Fatalf("missing WWW-Authenticate")
				}
			}
		})
	}
}
func TestPersistentTripCreateUsesResolvedOwner(t *testing.T) {
	owner := uuid.New()
	id := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	called := 0
	repo := tripRepositoryFake{create: func(_ context.Context, gotOwner uuid.UUID, title, notes string) (trips.Trip, error) {
		called++
		if gotOwner != owner || title != "Japan" || notes != "memo" {
			t.Fatalf("create args: owner=%s title=%q notes=%q", gotOwner, title, notes)
		}
		return trips.Trip{ID: id, OwnerID: owner, Title: title, Notes: notes, CreatedAt: now}, nil
	}}
	h := persistentTestHandler(t, owner, repo)
	rec := tripRequest(h, "POST", "/api/trips", `{"title":"  Japan  ","notes":"memo"}`, "Bearer valid")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if called != 1 {
		t.Fatalf("create calls=%d", called)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != id.String() || got["photo_count"] != float64(0) || got["cover"] != nil || got["owner_id"] != nil {
		t.Fatalf("unexpected response: %v", got)
	}
}
func TestPersistentTripListAndGet(t *testing.T) {
	owner, tripID := uuid.New(), uuid.New()
	record := trips.Trip{ID: tripID, OwnerID: owner, Title: "Japan", CreatedAt: time.Now().UTC()}
	repo := tripRepositoryFake{
		list: func(_ context.Context, got uuid.UUID) ([]trips.Trip, error) {
			if got != owner {
				t.Fatal("wrong list owner")
			}
			return []trips.Trip{record}, nil
		},
		get: func(_ context.Context, got, id uuid.UUID) (trips.Trip, error) {
			if got != owner || id != tripID {
				t.Fatal("wrong get args")
			}
			return record, nil
		},
	}
	h := persistentTestHandler(t, owner, repo)
	list := tripRequest(h, "GET", "/api/trips", "", "Bearer valid")
	if list.Code != 200 {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}
	var page tripPage
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != tripID.String() || page.NextCursor != nil {
		t.Fatalf("page=%+v", page)
	}
	get := tripRequest(h, "GET", "/api/trips/"+tripID.String(), "", "Bearer valid")
	if get.Code != 200 {
		t.Fatalf("get=%d %s", get.Code, get.Body.String())
	}
}
func TestPersistentTripNotFoundAndStorageErrors(t *testing.T) {
	owner, id := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not found", trips.ErrNotFound, 404, "not_found"},
		{"database error", errors.New("secret sql detail"), 500, "internal_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := persistentTestHandler(t, owner, tripRepositoryFake{get: func(context.Context, uuid.UUID, uuid.UUID) (trips.Trip, error) { return trips.Trip{}, tc.err }})
			rec := tripRequest(h, "GET", "/api/trips/"+id.String(), "", "Bearer valid")
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret sql detail") {
				t.Fatal("leaked database error")
			}
		})
	}
}
func TestPersistentTripCreateRejectsInvalidJSON(t *testing.T) {
	owner := uuid.New()
	h := persistentTestHandler(t, owner, tripRepositoryFake{})
	for _, body := range []string{`{"title":"Japan"}{"title":"Other"}`, `{"title":"Japan"} garbage`, `{"title":"Japan","owner_id":"` + uuid.NewString() + `"}`, `{"title":" "}`, `{"title":"` + strings.Repeat("a", 121) + `"}`} {
		rec := tripRequest(h, "POST", "/api/trips", body, "Bearer valid")
		if rec.Code != 400 {
			t.Fatalf("body=%q status=%d response=%s", body, rec.Code, rec.Body.String())
		}
	}
}
func TestPersistentTripListEmpty(t *testing.T) {
	owner := uuid.New()
	h := persistentTestHandler(t, owner, tripRepositoryFake{list: func(context.Context, uuid.UUID) ([]trips.Trip, error) { return nil, nil }})
	rec := tripRequest(h, "GET", "/api/trips", "", "Bearer valid")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
	}
}
