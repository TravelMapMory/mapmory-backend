package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TravelMapMory/mapmory-backend/internal/place"
	"github.com/TravelMapMory/mapmory-backend/internal/shares"
	"github.com/google/uuid"
)

const (
	shareTitleMax       = 120
	shareDescriptionMax = 600
	sharePhotosMax      = 500
	// Bound external city-centre lookups across the entire creation request.
	shareLookupBudget = 10 * time.Second
	// A share write should finish even if its client disconnects, but not run indefinitely.
	shareWriteTimeout = 5 * time.Second
	// publicPointLimit bounds the photos one public-map request clusters.
	publicPointLimit = 20000
	// publicMapPerSecond caps public-map requests across all callers.
	publicMapPerSecond = 20
)

// shareHandlers serves link and public shares (backend #19, #24, #25; design
// doc 4.2). Owner routes are protected; recipient and public routes are not,
// and they only ever return selected photos at coarse positions.
type shareHandlers struct {
	repo    shares.Repository
	uploads *uploadHandlers
	// places finds city centres for coarse positions; nil rounds to 0.01°.
	places        place.Searcher
	publicLimiter *rateLimiter
}

func newShareHandlers(repo shares.Repository, uploads *uploadHandlers, places place.Searcher) *shareHandlers {
	return &shareHandlers{repo: repo, uploads: uploads, places: places,
		publicLimiter: newRateLimiter(publicMapPerSecond, publicMapPerSecond)}
}

type apiShare struct {
	ID          string     `json:"id"`
	TripID      string     `json:"trip_id"`
	Mode        string     `json:"mode"`
	Title       string     `json:"title"`
	Description *string    `json:"description"`
	PhotoIDs    []string   `json:"photo_ids"`
	Token       string     `json:"token"`
	CreatedAt   time.Time  `json:"created_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
}

type shareList struct {
	Items []apiShare `json:"items"`
}

type apiSharedPhoto struct {
	ID               string        `json:"id"`
	DisplayImage     *displayImage `json:"display_image"`
	CaptureTimeLocal *string       `json:"capture_time_local"`
	City             *string       `json:"city"`
	Country          *string       `json:"country"`
	DisplayLat       *float64      `json:"display_lat"`
	DisplayLng       *float64      `json:"display_lng"`
}

type apiSharedCollection struct {
	Title       string           `json:"title"`
	Description *string          `json:"description"`
	CreatedAt   time.Time        `json:"created_at"`
	Photos      []apiSharedPhoto `json:"photos"`
}

type apiPublicShareRef struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	CoverURL   *string `json:"cover_url"`
	PhotoCount int     `json:"photo_count"`
}

type apiPublicCluster struct {
	ID         string              `json:"id"`
	Lat        float64             `json:"lat"`
	Lng        float64             `json:"lng"`
	Count      int                 `json:"count"`
	Label      *string             `json:"label"`
	CoverURL   *string             `json:"cover_url"`
	Bounds     [4]float64          `json:"bounds"`
	ShareCount int                 `json:"share_count"`
	Shares     []apiPublicShareRef `json:"shares"`
}

type apiPublicMap struct {
	Clusters  []apiPublicCluster `json:"clusters"`
	Truncated bool               `json:"truncated"`
}

func shareResponse(s shares.Share) apiShare {
	ids := make([]string, len(s.PhotoIDs))
	for i, id := range s.PhotoIDs {
		ids[i] = id.String()
	}
	return apiShare{ID: s.ID.String(), TripID: s.TripID.String(), Mode: s.Mode, Title: s.Title,
		Description: s.Description, PhotoIDs: ids, Token: s.Token, CreatedAt: s.CreatedAt, RevokedAt: s.RevokedAt}
}

// createShareInput is the validated POST /api/trips/{trip_id}/shares body.
type createShareInput struct {
	mode        string
	title       string
	description *string
	photoIDs    []uuid.UUID
}

func decodeCreateShare(body io.Reader) (createShareInput, string) {
	var raw struct {
		Mode        *string   `json:"mode"`
		Title       *string   `json:"title"`
		Description *string   `json:"description"`
		PhotoIDs    *[]string `json:"photo_ids"`
	}
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return createShareInput{}, "invalid request body"
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return createShareInput{}, "invalid request body"
	}
	var input createShareInput
	switch {
	case raw.Mode == nil || (*raw.Mode != shares.ModeLink && *raw.Mode != shares.ModePublic):
		return input, "mode must be link or public"
	case raw.Title == nil:
		return input, "title is required"
	case raw.PhotoIDs == nil || len(*raw.PhotoIDs) == 0:
		return input, "choose at least one photo"
	case len(*raw.PhotoIDs) > sharePhotosMax:
		return input, "a share can hold at most 500 photos"
	}
	input.mode = *raw.Mode
	input.title = strings.TrimSpace(*raw.Title)
	if n := utf8.RuneCountInString(input.title); n == 0 || n > shareTitleMax {
		return input, "title must be 1 to 120 characters"
	}
	if raw.Description != nil {
		description := strings.TrimSpace(*raw.Description)
		if utf8.RuneCountInString(description) > shareDescriptionMax {
			return input, "description must be at most 600 characters"
		}
		if description != "" {
			input.description = &description
		}
	}
	seen := make(map[uuid.UUID]bool)
	for _, value := range *raw.PhotoIDs {
		id, err := uuid.Parse(value)
		if err != nil {
			return input, "photo_ids must be photo IDs"
		}
		if !seen[id] {
			seen[id] = true
			input.photoIDs = append(input.photoIDs, id)
		}
	}
	return input, ""
}

// cityCentre looks up the centre of city, country once per create request.
// A missing searcher, a provider failure or no exact match returns nil, and
// the caller rounds the position instead.
func (h *shareHandlers) cityCentre(ctx context.Context, cache map[string]*[2]float64, city, country string) *[2]float64 {
	if ctx.Err() != nil {
		return nil
	}
	key := city + "\x00" + country
	if centre, ok := cache[key]; ok {
		return centre
	}
	var centre *[2]float64
	if h.places != nil {
		lookup, cancel := context.WithTimeout(ctx, labelLookupTimeout)
		defer cancel()
		query := city
		if country != "" {
			query += ", " + country
		}
		candidates, err := h.places.Search(lookup, query, 5)
		if err != nil {
			log.Printf("share city centre: %v", err)
		}
		for _, c := range candidates {
			if c.Kind == place.KindCity && strings.EqualFold(c.City, city) && (country == "" || strings.EqualFold(c.Country, country)) {
				centre = &[2]float64{c.Lat, c.Lng}
				break
			}
		}
	}
	cache[key] = centre
	return centre
}

func (h *shareHandlers) create(w http.ResponseWriter, r *http.Request) {
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
	input, message := decodeCreateShare(r.Body)
	if message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	ctx := r.Context()
	candidates, err := h.repo.Candidates(ctx, owner, tripID, input.photoIDs)
	if errors.Is(err, shares.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	if err != nil {
		uploadInternalError(w, "share candidates", err)
		return
	}
	if len(candidates) != len(input.photoIDs) {
		writeError(w, http.StatusBadRequest, "invalid_request", "choose processed photos of this trip")
		return
	}

	lookupCtx, cancelLookup := context.WithTimeout(ctx, shareLookupBudget)
	defer cancelLookup()
	centres := make(map[string]*[2]float64)
	byID := make(map[uuid.UUID]shares.Candidate, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
	}
	selected := make([]shares.Selected, 0, len(input.photoIDs))
	for _, id := range input.photoIDs {
		c := byID[id]
		s := shares.Selected{PhotoID: id}
		if c.Lat != nil && c.Lng != nil {
			var centre *[2]float64
			if c.City != nil && *c.City != "" {
				country := ""
				if c.Country != nil {
					country = *c.Country
				}
				centre = h.cityCentre(lookupCtx, centres, *c.City, country)
			}
			lat, lng := shares.CoarsePosition(*c.Lat, *c.Lng, centre)
			s.DisplayLat, s.DisplayLng = &lat, &lng
		}
		selected = append(selected, s)
	}

	writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), shareWriteTimeout)
	defer cancelWrite()
	share, err := h.repo.Create(writeCtx, owner, shares.NewShare{
		TripID: tripID, Mode: input.mode, Title: input.title, Description: input.description, Photos: selected,
	})
	switch {
	case errors.Is(err, shares.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
	case errors.Is(err, shares.ErrInvalidPhotos):
		writeError(w, http.StatusBadRequest, "invalid_request", "choose processed photos of this trip")
	case err != nil:
		uploadInternalError(w, "share create", err)
	default:
		writeJSON(w, http.StatusCreated, shareResponse(share))
	}
}

func (h *shareHandlers) list(w http.ResponseWriter, r *http.Request) {
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
	records, err := h.repo.ListByTrip(r.Context(), owner, tripID)
	if errors.Is(err, shares.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	if err != nil {
		uploadInternalError(w, "share list", err)
		return
	}
	response := shareList{Items: make([]apiShare, 0, len(records))}
	for _, record := range records {
		response.Items = append(response.Items, shareResponse(record))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *shareHandlers) revoke(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	shareID, err := uuid.Parse(r.PathValue("share_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	err = h.repo.Revoke(r.Context(), owner, shareID)
	if errors.Is(err, shares.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	if err != nil {
		uploadInternalError(w, "share revoke", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeCollection signs the selected photos' display images and writes what a
// recipient sees. Responses are never cached: revoking must take effect at once.
func (h *shareHandlers) writeCollection(w http.ResponseWriter, c shares.Collection) {
	names := make([]string, len(c.Photos))
	for i, p := range c.Photos {
		names[i] = p.DisplayObjectName
	}
	images, err := h.uploads.signDisplays(names)
	if err != nil {
		uploadInternalError(w, "display signing", err)
		return
	}
	response := apiSharedCollection{Title: c.Title, Description: c.Description, CreatedAt: c.CreatedAt,
		Photos: make([]apiSharedPhoto, 0, len(c.Photos))}
	for i, p := range c.Photos {
		response.Photos = append(response.Photos, apiSharedPhoto{
			ID: p.ID.String(), DisplayImage: images[i], CaptureTimeLocal: p.CaptureTimeLocal,
			City: p.City, Country: p.Country, DisplayLat: p.DisplayLat, DisplayLng: p.DisplayLng,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

// byToken serves GET /api/s/{token} to anyone holding the link.
func (h *shareHandlers) byToken(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if len(token) < 22 || len(token) > 64 {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	c, err := h.repo.ByToken(r.Context(), token)
	switch {
	case errors.Is(err, shares.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "share not found")
	case errors.Is(err, shares.ErrRevoked):
		writeError(w, http.StatusGone, "share_revoked", "this link was turned off")
	case err != nil:
		uploadInternalError(w, "shared collection", err)
	default:
		h.writeCollection(w, c)
	}
}

// publicShare serves GET /api/public/shares/{share_id} for live public shares.
func (h *shareHandlers) publicShare(w http.ResponseWriter, r *http.Request) {
	shareID, err := uuid.Parse(r.PathValue("share_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	c, err := h.repo.PublicByID(r.Context(), shareID)
	if errors.Is(err, shares.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	if err != nil {
		uploadInternalError(w, "public share", err)
		return
	}
	h.writeCollection(w, c)
}

func parsePublicMapQuery(r *http.Request) ([4]float64, int, string) {
	var box [4]float64
	parts := strings.Split(r.URL.Query().Get("bbox"), ",")
	if len(parts) != 4 {
		return box, 0, "bbox must be west,south,east,north"
	}
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return box, 0, "bbox must be four numbers"
		}
		box[i] = value
	}
	if box[0] < -180 || box[0] > 180 || box[2] < -180 || box[2] > 180 ||
		box[1] < -90 || box[3] > 90 || box[1] > box[3] {
		return box, 0, "bbox is out of range"
	}
	zoom, err := strconv.Atoi(r.URL.Query().Get("zoom"))
	if err != nil || zoom < 0 || zoom > shares.MaxZoom {
		return box, 0, "zoom must be an integer from 0 to 22"
	}
	return box, zoom, ""
}

// publicMap serves GET /api/public/map: clusters of photos from live public
// shares in the visible box (design doc 5.2), at most 200.
func (h *shareHandlers) publicMap(w http.ResponseWriter, r *http.Request) {
	box, zoom, message := parsePublicMapQuery(r)
	if message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	if !h.publicLimiter.allow() {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many map requests; try again shortly")
		return
	}
	// Fetch one extra row to distinguish a complete result from a capped input.
	points, err := h.repo.PublicPoints(r.Context(), box[0], box[1], box[2], box[3], publicPointLimit+1)
	if err != nil {
		uploadInternalError(w, "public map", err)
		return
	}
	inputTruncated := len(points) > publicPointLimit
	if inputTruncated {
		points = points[:publicPointLimit]
	}
	clusters, clustersTruncated := shares.ClusterPoints(points, zoom, shares.ClusterLimit)
	truncated := inputTruncated || clustersTruncated

	// Sign each distinct cover once.
	index := make(map[string]int)
	names := make([]string, 0)
	need := func(name string) {
		if _, ok := index[name]; !ok && name != "" {
			index[name] = len(names)
			names = append(names, name)
		}
	}
	for _, c := range clusters {
		need(c.CoverObject)
		for _, s := range c.Shares {
			need(s.CoverObject)
		}
	}
	images, err := h.uploads.signDisplays(names)
	if err != nil {
		uploadInternalError(w, "display signing", err)
		return
	}
	url := func(name string) *string {
		if i, ok := index[name]; ok && images[i] != nil {
			return &images[i].URL
		}
		return nil
	}
	response := apiPublicMap{Clusters: make([]apiPublicCluster, 0, len(clusters)), Truncated: truncated}
	for _, c := range clusters {
		cluster := apiPublicCluster{ID: c.ID, Lat: c.Lat, Lng: c.Lng, Count: c.Count, CoverURL: url(c.CoverObject),
			Bounds: c.Bounds, ShareCount: c.ShareCount, Shares: make([]apiPublicShareRef, 0, len(c.Shares))}
		if c.Label != "" {
			label := c.Label
			cluster.Label = &label
		}
		for _, s := range c.Shares {
			cluster.Shares = append(cluster.Shares, apiPublicShareRef{ID: s.ID.String(), Title: s.Title,
				CoverURL: url(s.CoverObject), PhotoCount: s.PhotoCount})
		}
		response.Clusters = append(response.Clusters, cluster)
	}
	writeJSON(w, http.StatusOK, response)
}
