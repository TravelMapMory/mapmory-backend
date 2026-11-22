package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TravelMapMory/mapmory-backend/internal/appuser"
	"github.com/TravelMapMory/mapmory-backend/internal/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/photos"
	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/google/uuid"
)

// persistentTripHandlers handles owner-scoped trips in database-backed mode.
// The repository is shared across requests and must not be closed by handlers.
type persistentTripHandlers struct {
	repository trips.Repository
	// overview and sign add photo aggregates and the cover to trip responses;
	// both are nil when photo uploads are disabled.
	overview photos.Overview
	sign     func(objectName string) (string, time.Time, error)
}

// tripResponse is a trip without photos: a new trip, or uploads disabled.
func tripResponse(record trips.Trip) ownedTrip {
	return ownedTrip{
		trip: trip{ID: record.ID.String(), Title: record.Title, Notes: record.Notes,
			CreatedAt: record.CreatedAt, PhotoCount: 0, Cover: nil},
		Cities: []string{}, Countries: []string{},
	}
}

// tripResponses adds each trip's photo aggregates and signed cover.
func (h *persistentTripHandlers) tripResponses(ctx context.Context, owner uuid.UUID, records []trips.Trip) ([]ownedTrip, error) {
	items := make([]ownedTrip, 0, len(records))
	for _, record := range records {
		items = append(items, tripResponse(record))
	}
	if h.overview == nil || len(records) == 0 {
		return items, nil
	}
	ids := make([]uuid.UUID, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	stats, err := h.overview.TripStats(ctx, owner, ids)
	if err != nil {
		return nil, err
	}
	for i := range items {
		s := stats[records[i].ID]
		items[i].PhotoCount, items[i].LocatedCount = s.PhotoCount, s.LocatedCount
		items[i].StartDate, items[i].EndDate = s.StartDate, s.EndDate
		if s.Cities != nil {
			items[i].Cities = s.Cities
		}
		if s.Countries != nil {
			items[i].Countries = s.Countries
		}
		if s.Cover != nil && s.Cover.DisplayObjectName != nil {
			url, expires, err := h.sign(*s.Cover.DisplayObjectName)
			if err != nil {
				return nil, err
			}
			items[i].Cover = &tripCover{PhotoID: s.Cover.ID.String(), Image: displayImage{URL: url, ExpiresAt: expires}}
		}
	}
	return items, nil
}

// writeTrip writes one trip with its aggregates.
func (h *persistentTripHandlers) writeTrip(w http.ResponseWriter, r *http.Request, owner uuid.UUID, record trips.Trip) {
	items, err := h.tripResponses(r.Context(), owner, []trips.Trip{record})
	if err != nil {
		tripStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items[0])
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
	items, err := h.tripResponses(r.Context(), owner, records)
	if err != nil {
		tripStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ownedTripPage{Items: items, NextCursor: nil})
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
	h.writeTrip(w, r, owner, record)
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
	h.writeTrip(w, r, owner, record)
}

// newHandlerWithPersistentTrips registers only database-backed routes in
// database mode. In particular, no legacy in-memory trip router is mounted:
// unsupported methods and alternate paths cannot reach prototype storage.
func newHandlerWithPersistentTrips(verifier auth.Verifier, resolver appuser.Resolver, repository trips.Repository) http.Handler {
	return newPersistentHandler(verifier, resolver, repository, nil)
}

// newPersistentHandler also registers upload routes when uploads is non-nil,
// that is, when a photo bucket is configured.
func newPersistentHandler(verifier auth.Verifier, resolver appuser.Resolver, repository trips.Repository, uploads *uploadHandlers) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.Handle("GET /api/auth/me", auth.RequireIdentity(verifier, http.HandlerFunc(authMeHandler)))
	mux.Handle("GET /api/users/me", auth.RequireIdentity(verifier,
		appuser.RequireIdentity(resolver, http.HandlerFunc(userMeHandler))))

	handlers := &persistentTripHandlers{repository: repository}
	if uploads != nil {
		handlers.overview, handlers.sign = uploads.overview, uploads.store.SignDisplay
	}
	protected := func(next http.HandlerFunc) http.Handler {
		return auth.RequireIdentity(verifier, appuser.RequireIdentity(resolver, next))
	}
	mux.Handle("GET /api/trips", protected(handlers.list))
	mux.Handle("POST /api/trips", protected(handlers.create))
	mux.Handle("GET /api/trips/{trip_id}", protected(handlers.get))
	mux.Handle("PATCH /api/trips/{trip_id}", protected(handlers.update))
	if uploads != nil {
		mux.Handle("POST /api/trips/{trip_id}/batches", protected(uploads.registerBatch))
		mux.Handle("GET /api/trips/{trip_id}/photos", protected(uploads.listTripPhotos))
		mux.Handle("GET /api/photos/{photo_id}", protected(uploads.getPhoto))
		mux.Handle("PATCH /api/photos/{photo_id}/location", protected(uploads.updatePhotoLocation))
		if s := uploads.shares; s != nil {
			mux.Handle("GET /api/trips/{trip_id}/shares", protected(s.list))
			mux.Handle("POST /api/trips/{trip_id}/shares", protected(s.create))
			mux.Handle("DELETE /api/shares/{share_id}", protected(s.revoke))
			// Recipient and public views need no account (design doc 2.1).
			mux.HandleFunc("GET /api/s/{token}", s.byToken)
			mux.HandleFunc("GET /api/public/shares/{share_id}", s.publicShare)
			mux.HandleFunc("GET /api/public/map", s.publicMap)
		}
		if uploads.overview != nil {
			mux.Handle("GET /api/map/photos", protected(uploads.mapPhotos))
			mux.Handle("GET /api/me/summary", protected(uploads.summary))
		}
		mux.Handle("GET /api/batches/{batch_id}", protected(uploads.getBatch))
		mux.Handle("POST /api/batches/{batch_id}/files/{client_file_id}/complete", protected(uploads.completeUpload))
	}
	return mux
}
