package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestHealthHandler checks the health endpoint's status, content type and body,
// the contract that CI and the deployment platform rely on.
func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	newHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	var got healthResponse

	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if got.Status != "ok" {
		t.Errorf("status = %q, want %q", got.Status, "ok")
	}
}

// TestListTripsEmpty checks that the trips listing endpoint returns an empty list of trips
// and a nil next_cursor when there are no trips.
func TestListTripsEmpty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/trips", nil)
	rec := httptest.NewRecorder()

	newHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	var got tripPage
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if got.Items == nil {
		t.Error("items is nil, want empty non-nil slice")
	}

	if len(got.Items) != 0 {
		t.Errorf("len(items) = %d, want 0", len(got.Items))
	}

	if got.NextCursor != nil {
		t.Errorf("next_cursor = %q, want nil", *got.NextCursor)
	}
}

// TestCreateTripValidation checks that the create trip endpoint returns appropriate
// error responses for various invalid request bodies and titles.
func TestCreateTripValidation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		message string
	}{
		{
			name:    "malformed JSON",
			body:    `{"title":`,
			message: "invalid request body",
		},
		{
			name:    "unknown field",
			body:    `{"title":"Japan","titel":"oops"}`,
			message: "invalid request body",
		},
		{
			name:    "missing title",
			body:    `{}`,
			message: "title must not be blank",
		},
		{
			name:    "blank title",
			body:    `{"title":""}`,
			message: "title must not be blank",
		},
		{
			name:    "whitespace-only title",
			body:    `{"title":"   "}`,
			message: "title must not be blank",
		},
		{
			name:    "title too long",
			body:    `{"title":"` + strings.Repeat("a", 121) + `"}`,
			message: "title must be at most 120 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/trips",
				strings.NewReader(tt.body),
			)
			req.Header.Set("Content-Type", "application/json")

			rec := httptest.NewRecorder()

			newHandler().ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf(
					"status = %d, want %d",
					rec.Code,
					http.StatusBadRequest,
				)
			}

			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf(
					"Content-Type = %q, want %q",
					got,
					"application/json",
				)
			}

			var got errorResponse
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}

			if got.Error.Code != "invalid_request" {
				t.Errorf(
					"error.code = %q, want %q",
					got.Error.Code,
					"invalid_request",
				)
			}

			if got.Error.Message != tt.message {
				t.Errorf(
					"error.message = %q, want %q",
					got.Error.Message,
					tt.message,
				)
			}
		})
	}
}

// TestCreateAndListTrip checks that a trip can be created and then listed,
// verifying the fields of the created trip.
func TestCreateAndListTrip(t *testing.T) {
	handler := newHandler()

	postReq := httptest.NewRequest(
		http.MethodPost,
		"/api/trips",
		strings.NewReader(`{
			"title": "  Japan  ",
			"notes": "Summer trip"
		}`),
	)
	postReq.Header.Set("Content-Type", "application/json")

	postRec := httptest.NewRecorder()

	handler.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusCreated {
		t.Fatalf(
			"POST status = %d, want %d",
			postRec.Code,
			http.StatusCreated,
		)
	}

	var created trip
	if err := json.NewDecoder(postRec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	if created.Title != "Japan" {
		t.Errorf(
			"title = %q, want %q",
			created.Title,
			"Japan",
		)
	}

	if created.Notes != "Summer trip" {
		t.Errorf(
			"notes = %q, want %q",
			created.Notes,
			"Summer trip",
		)
	}

	if _, err := uuid.Parse(created.ID); err != nil {
		t.Errorf("id = %q, want valid UUID: %v", created.ID, err)
	}

	if created.CreatedAt.IsZero() {
		t.Error("created_at is zero")
	}

	if created.PhotoCount != 0 {
		t.Errorf(
			"photo_count = %d, want 0",
			created.PhotoCount,
		)
	}

	if created.Cover != nil {
		t.Error("cover is non-nil, want nil")
	}

	getReq := httptest.NewRequest(
		http.MethodGet,
		"/api/trips",
		nil,
	)

	getRec := httptest.NewRecorder()

	handler.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf(
			"GET status = %d, want %d",
			getRec.Code,
			http.StatusOK,
		)
	}

	var page tripPage
	if err := json.NewDecoder(getRec.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}

	if len(page.Items) != 1 {
		t.Fatalf(
			"len(items) = %d, want 1",
			len(page.Items),
		)
	}

	got := page.Items[0]

	if got.ID != created.ID {
		t.Errorf(
			"listed id = %q, want %q",
			got.ID,
			created.ID,
		)
	}

	if got.Title != created.Title {
		t.Errorf(
			"listed title = %q, want %q",
			got.Title,
			created.Title,
		)
	}
}

// TestGetTrip verifies that a newly created trip is retrievable by its ID.
func TestGetTrip(t *testing.T) {
	handler := newHandler()
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/trips", strings.NewReader(`{"title":"debugging"}`)))
	if post.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", post.Code, post.Body.String())
	}
	var created trip
	if err := json.Unmarshal(post.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/trips/"+created.ID, nil))
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", get.Code, get.Body.String())
	}
	if got := get.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content type = %q", got)
	}
	var result trip
	if err := json.Unmarshal(get.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result != created {
		t.Errorf("retrieved trip = %+v, want %+v", result, created)
	}
}

func TestGetTripNotFound(t *testing.T) {
	handler := newHandler()
	for _, id := range []string{"00000000-0000-4000-8000-000000000000", "123"} {
		t.Run(id, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/trips/"+id, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("content type = %q", ct)
			}
			var got errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Error.Code != "not_found" {
				t.Errorf("error code = %q, want not_found", got.Error.Code)
			}
		})
	}
}
