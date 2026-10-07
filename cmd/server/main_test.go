package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"uuid"
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
