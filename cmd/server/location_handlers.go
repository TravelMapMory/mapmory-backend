package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/TravelMapMory/mapmory-backend/internal/photos"
	"github.com/TravelMapMory/mapmory-backend/internal/place"
	"github.com/google/uuid"
)

const (
	// labelLookupTimeout is the maximum time to wait for a place resolver to
	// return labels for a photo's coordinates. The resolver is called in the
	// background, so a failure does not block the upload.
	labelLookupTimeout = 8 * time.Second
	// locationWriteTimeout is the maximum time to wait for a photo location
	// update to be stored. The update is atomic, so a failure leaves the photo
	// in its previous state.
	locationWriteTimeout = 5 * time.Second
)

// Place search limits (design doc 5.2: debounced and rate-limited).
const (
	placeSearchMinRunes = 2
	placeSearchMaxRunes = 100
	placeSearchLimit    = 8
	// placeSearchPerSecond caps provider calls across all callers, so a public
	// endpoint cannot exhaust the Geoapify quota.
	placeSearchPerSecond = 5
)

// labelsFrom turns a resolver result into stored labels and their credit.
func labelsFrom(resolved place.ResolvedPlace) photos.Labels {
	credits := make([]string, 0, len(resolved.Attributions))
	for _, attribution := range resolved.Attributions {
		credits = append(credits, attribution.Text)
	}
	return photos.Labels{
		Country:     resolved.Country,
		City:        resolved.City,
		PlaceName:   resolved.Name,
		Provider:    resolved.Provider,
		Attribution: strings.Join(credits, " · "),
	}
}

// lookupLabels resolves lat, lng. It returns nil, never an error, when no
// resolver is configured or the provider fails: labels then stay pending and
// the photo itself is unaffected (design doc 7.3). Coordinates are personal
// data and never logged.
func (h *uploadHandlers) lookupLabels(ctx context.Context, photoID uuid.UUID, lat, lng float64) *photos.Labels {
	if h.places == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, labelLookupTimeout)
	defer cancel()
	resolved, err := h.places.Resolve(ctx, lat, lng)
	if err != nil {
		if !errors.Is(err, place.ErrNoResult) {
			log.Printf("photo %s: place lookup: %v", photoID, err)
		}
		return nil
	}
	labels := labelsFrom(resolved)
	return &labels
}

// suggestLabels adds resolver labels to a freshly processed photo with a
// position. Failures keep the photo as processed, with labels pending.
func (h *uploadHandlers) suggestLabels(ctx context.Context, photo photos.Photo) photos.Photo {
	if photo.Lat == nil || photo.Lng == nil {
		return photo
	}
	labels := h.lookupLabels(ctx, photo.ID, *photo.Lat, *photo.Lng)
	if labels == nil {
		return photo
	}
	stored, err := h.photos.SuggestLabels(ctx, photo.ID, *photo.Lat, *photo.Lng, *labels)
	if err != nil {
		log.Printf("photo %s: store suggested labels: %v", photo.ID, err)
		return photo
	}
	if stored {
		photo.Country, photo.City, photo.PlaceName = nullable(labels.Country), nullable(labels.City), nullable(labels.PlaceName)
		photo.LabelProvider, photo.LabelAttribution = nullable(labels.Provider), nullable(labels.Attribution)
		photo.LabelStatus = photos.LabelSuggested
	}
	return photo
}

func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// locationPatch is the body of PATCH /api/photos/{photo_id}/location: either a
// new position, or acceptance of the suggested labels.
type locationPatch struct {
	Lat          *float64 `json:"lat"`
	Lng          *float64 `json:"lng"`
	ConfirmLabel *bool    `json:"confirm_label"`
}

func decodeLocationPatch(body io.Reader) (locationPatch, string) {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var patch locationPatch
	if err := decoder.Decode(&patch); err != nil {
		return locationPatch{}, "invalid request body"
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return locationPatch{}, "invalid request body"
	}
	switch {
	case patch.ConfirmLabel != nil && (patch.Lat != nil || patch.Lng != nil):
		return locationPatch{}, "send either lat and lng, or confirm_label"
	case patch.ConfirmLabel != nil:
		if !*patch.ConfirmLabel {
			return locationPatch{}, "confirm_label must be true"
		}
	case patch.Lat == nil || patch.Lng == nil:
		return locationPatch{}, "lat and lng are both required"
	case math.IsNaN(*patch.Lat) || *patch.Lat < -90 || *patch.Lat > 90 ||
		math.IsNaN(*patch.Lng) || *patch.Lng < -180 || *patch.Lng > 180:
		return locationPatch{}, "lat must be within -90..90 and lng within -180..180"
	}
	return patch, ""
}

// updatePhotoLocation saves an owner's location correction (backend #43,
// design doc 4.1). A new position becomes the effective lat/lng; the EXIF pair
// and capture time never change. The position's labels are resolved and stored
// as confirmed, because the owner chose the place; if the provider fails they
// stay pending.
func (h *uploadHandlers) updatePhotoLocation(w http.ResponseWriter, r *http.Request) {
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
	patch, message := decodeLocationPatch(r.Body)
	if message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	ctx := r.Context()

	var photo photos.Photo
	if patch.ConfirmLabel != nil {
		photo, err = h.photos.ConfirmLabels(ctx, owner, photoID)
	} else {
		// Check ownership and state first, so a refused request never spends
		// provider quota. Relocate checks the state again atomically.
		current, err := h.photos.Get(ctx, owner, photoID)
		if err != nil {
			h.writeLocationError(w, err)
			return
		}
		if current.State != photos.StateReady && current.State != photos.StateNeedsLocation {
			h.writeLocationError(w, photos.ErrNotProcessed)
			return
		}
		labels := h.lookupLabels(ctx, photoID, *patch.Lat, *patch.Lng)
		writeCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			locationWriteTimeout,
		)
		defer cancel()

		photo, err = h.photos.Relocate(
			writeCtx,
			owner,
			photoID,
			*patch.Lat,
			*patch.Lng,
			labels,
		)
	}
	if err != nil {
		h.writeLocationError(w, err)
		return
	}
	h.writePhoto(w, photo)
}

func (h *uploadHandlers) writeLocationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, photos.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "photo not found")
	case errors.Is(err, photos.ErrNotProcessed):
		writeError(w, http.StatusConflict, "photo_not_processed", "only a processed photo can be given a location")
	case errors.Is(err, photos.ErrNoSuggestion):
		writeError(w, http.StatusConflict, "no_suggested_labels", "the photo has no suggested place to confirm")
	default:
		uploadInternalError(w, "photo location update", err)
	}
}

// placeSearchHandler serves GET /api/places/search (design doc 4, "Find a
// place"). It is public, like the public map that uses it, and rate-limited.
type placeSearchHandler struct {
	searcher place.Searcher
	limiter  *rateLimiter
}

type apiPlace struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Label   string  `json:"label"`
	Kind    string  `json:"kind"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	Zoom    int     `json:"zoom"`
	City    *string `json:"city"`
	Country string  `json:"country"`
}

type placeSearchResponse struct {
	Items []apiPlace `json:"items"`
}

func (h *placeSearchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if n := utf8.RuneCountInString(query); n < placeSearchMinRunes || n > placeSearchMaxRunes {
		writeError(w, http.StatusBadRequest, "invalid_request", "q must be 2 to 100 characters")
		return
	}
	if h.searcher == nil {
		writeError(w, http.StatusServiceUnavailable, "places_unavailable", "place search is not configured")
		return
	}
	if !h.limiter.allow() {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many place searches; try again shortly")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), labelLookupTimeout)
	defer cancel()
	candidates, err := h.searcher.Search(ctx, query, placeSearchLimit)
	if err != nil {
		// Never report a provider failure as "no results" (frontend #29).
		log.Printf("place search: %v", err)
		writeError(w, http.StatusServiceUnavailable, "places_unavailable", "place search is temporarily unavailable")
		return
	}
	response := placeSearchResponse{Items: make([]apiPlace, 0, len(candidates))}
	for _, c := range candidates {
		item := apiPlace{ID: c.ID, Name: c.Name, Label: c.Label, Kind: c.Kind, Lat: c.Lat, Lng: c.Lng, Country: c.Country, Zoom: 5}
		if c.Kind == place.KindCity {
			item.Zoom, item.City = 11, nullable(c.City)
		}
		response.Items = append(response.Items, item)
	}
	writeJSON(w, http.StatusOK, response)
}

// rateLimiter is a token bucket shared by every caller of one endpoint.
type rateLimiter struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	rate   float64
	last   time.Time
	now    func() time.Time
}

func newRateLimiter(perSecond, burst float64) *rateLimiter {
	return &rateLimiter{tokens: burst, burst: burst, rate: perSecond, now: time.Now}
}

func (l *rateLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if !l.last.IsZero() {
		l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

const (
	// labelRetriesPerListing bounds provider calls one trip listing can make.
	labelRetriesPerListing = 5
	// labelRetryBudget bounds how long a listing waits for retries.
	labelRetryBudget = 4 * time.Second
)

// retryPendingLabels gives pending labels in a trip another chance (design
// doc 7.3) before its first page is listed. It never fails the listing: an
// error or a slow provider just leaves the labels pending for next time.
func (h *uploadHandlers) retryPendingLabels(ctx context.Context, owner, tripID uuid.UUID) {
	if h.places == nil {
		return
	}
	candidates, err := h.photos.ClaimLabelRetries(context.WithoutCancel(ctx), owner, tripID, labelRetriesPerListing)
	if err != nil {
		log.Printf("trip %s: claim label retries: %v", tripID, err)
		return
	}
	if len(candidates) == 0 {
		return
	}
	budget, cancel := context.WithTimeout(ctx, labelRetryBudget)
	defer cancel()
	var wg sync.WaitGroup
	for _, photo := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.suggestLabels(budget, photo)
		}()
	}
	wg.Wait()
}
