package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/TravelMapMory/mapmory-backend/internal/appuser"
	"github.com/TravelMapMory/mapmory-backend/internal/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/google/uuid"
)

// persistentTripHandlers handles owner-scoped trips in database-backed mode.
// The repository is shared across requests and must not be closed by handlers.
type persistentTripHandlers struct{ repository trips.Repository }

func tripResponse(record trips.Trip) trip {
	return trip{ID: record.ID.String(), Title: record.Title, Notes: record.Notes,
		CreatedAt: record.CreatedAt, PhotoCount: 0, Cover: nil}
}

func tripOwner(r *http.Request) (uuid.UUID, bool) {
	identity, ok := appuser.IdentityFromContext(r.Context())
	return identity.ID, ok
}

func tripStorageError(w http.ResponseWriter, err error) {
	log.Printf("trip storage operation failed: %v", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "an unexpected error occurred")
}

func (h *persistentTripHandlers) list(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	records, err := h.repository.ListByOwner(r.Context(), owner)
	if err != nil {
		tripStorageError(w, err)
		return
	}
	items := make([]trip, 0, len(records))
	for _, record := range records {
		items = append(items, tripResponse(record))
	}
	writeJSON(w, http.StatusOK, tripPage{Items: items, NextCursor: nil})
}

// normalizeTripTitle and normalizeTripNotes enforce the same rules for
// creation and partial updates. Lengths are measured after trimming.
func normalizeTripTitle(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "title must not be blank"
	}
	if utf8.RuneCountInString(value) > 120 {
		return "", "title must be at most 120 characters"
	}
	return value, ""
}

func normalizeTripNotes(value string) (string, string) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > 2000 {
		return "", "notes must be at most 2000 characters"
	}
	return value, ""
}

func (h *persistentTripHandlers) create(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	// Decode field-by-field to reject JSON null and unknown properties,
	// consistent with PATCH rather than silently treating null as missing.
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	var title, notes string
	hasTitle := false
	for key, value := range raw {
		if string(value) == "null" {
			writeError(w, http.StatusBadRequest, "invalid_request", "fields must not be null")
			return
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "fields must be strings")
			return
		}
		switch key {
		case "title":
			hasTitle = true
			var message string
			title, message = normalizeTripTitle(text)
			if message != "" {
				writeError(w, http.StatusBadRequest, "invalid_request", message)
				return
			}
		case "notes":
			var message string
			notes, message = normalizeTripNotes(text)
			if message != "" {
				writeError(w, http.StatusBadRequest, "invalid_request", message)
				return
			}
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown field")
			return
		}
	}
	if !hasTitle {
		writeError(w, http.StatusBadRequest, "invalid_request", "title must not be blank")
		return
	}
	record, err := h.repository.Create(r.Context(), owner, title, notes)
	if err != nil {
		tripStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tripResponse(record))
}

func (h *persistentTripHandlers) get(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, err := uuid.Parse(r.PathValue("trip_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	record, err := h.repository.GetByID(r.Context(), owner, id)
	if errors.Is(err, trips.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	if err != nil {
		tripStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tripResponse(record))
}

// update applies partial changes only to a trip owned by the resolved user.
func (h *persistentTripHandlers) update(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, err := uuid.Parse(r.PathValue("trip_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "at least one field is required")
		return
	}
	var fields trips.UpdateFields
	for key, value := range raw {
		if string(value) == "null" {
			writeError(w, http.StatusBadRequest, "invalid_request", "fields must not be null")
			return
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "fields must be strings")
			return
		}
		switch key {
		case "title":
			normalized, message := normalizeTripTitle(text)
			if message != "" {
				writeError(w, http.StatusBadRequest, "invalid_request", message)
				return
			}
			fields.Title = &normalized
		case "notes":
			normalized, message := normalizeTripNotes(text)
			if message != "" {
				writeError(w, http.StatusBadRequest, "invalid_request", message)
				return
			}
			fields.Notes = &normalized
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown field")
			return
		}
	}
	record, err := h.repository.Update(r.Context(), owner, id, fields)
	if errors.Is(err, trips.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	if err != nil {
		tripStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tripResponse(record))
}

// newHandlerWithPersistentTrips registers only database-backed routes in
// database mode. In particular, no legacy in-memory trip router is mounted:
// unsupported methods and alternate paths cannot reach prototype storage.
func newHandlerWithPersistentTrips(verifier auth.Verifier, resolver appuser.Resolver, repository trips.Repository) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.Handle("GET /api/auth/me", auth.RequireIdentity(verifier, http.HandlerFunc(authMeHandler)))
	mux.Handle("GET /api/users/me", auth.RequireIdentity(verifier,
		appuser.RequireIdentity(resolver, http.HandlerFunc(userMeHandler))))

	handlers := &persistentTripHandlers{repository: repository}
	protected := func(next http.HandlerFunc) http.Handler {
		return auth.RequireIdentity(verifier, appuser.RequireIdentity(resolver, next))
	}
	mux.Handle("GET /api/trips", protected(handlers.list))
	mux.Handle("POST /api/trips", protected(handlers.create))
	mux.Handle("GET /api/trips/{trip_id}", protected(handlers.get))
	mux.Handle("PATCH /api/trips/{trip_id}", protected(handlers.update))
	return mux
}
