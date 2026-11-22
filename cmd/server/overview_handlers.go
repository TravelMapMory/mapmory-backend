package main

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/TravelMapMory/mapmory-backend/internal/photos"
	"github.com/google/uuid"
)

const (
	// mapPhotoLimit caps one owner-map response (design doc 5.2); the browser
	// clusters the markers.
	mapPhotoLimit = 2000
	// recentUploads is how many photos the Dashboard shows as recently uploaded.
	recentUploads = 8
	// signParallelism bounds concurrent display-URL signatures. Each uncached
	// signature can be one IAM call; cached ones return at once.
	signParallelism = 16
)

type mapPhoto struct {
	ID               string        `json:"id"`
	TripID           string        `json:"trip_id"`
	TripTitle        string        `json:"trip_title"`
	Lat              float64       `json:"lat"`
	Lng              float64       `json:"lng"`
	DisplayImage     *displayImage `json:"display_image"`
	CaptureTimeLocal *string       `json:"capture_time_local"`
	City             *string       `json:"city"`
	Country          *string       `json:"country"`
	PlaceName        *string       `json:"place_name"`
}

type mapPhotoPage struct {
	Items     []mapPhoto `json:"items"`
	Truncated bool       `json:"truncated"`
}

type meSummary struct {
	PhotoCount       int        `json:"photo_count"`
	CountryCount     int        `json:"country_count"`
	CityCount        int        `json:"city_count"`
	ToReviewCount    int        `json:"to_review_count"`
	RecentlyUploaded []apiPhoto `json:"recently_uploaded"`
}

// signDisplays signs the display images of objectNames in parallel. An empty
// name yields nil. Any signing error fails the whole call.
func (h *uploadHandlers) signDisplays(objectNames []string) ([]*displayImage, error) {
	images := make([]*displayImage, len(objectNames))
	errs := make([]error, len(objectNames))
	slots := make(chan struct{}, signParallelism)
	var wg sync.WaitGroup
	for i, name := range objectNames {
		if name == "" {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			url, expires, err := h.store.SignDisplay(name)
			if err != nil {
				errs[i] = err
				return
			}
			images[i] = &displayImage{URL: url, ExpiresAt: expires}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return images, nil
}

// parseMapQuery reads the GET /api/map/photos query string. Every filter is
// optional; an invalid one is a 400 rather than being ignored.
func parseMapQuery(r *http.Request) (photos.MapQuery, string) {
	values := r.URL.Query()
	q := photos.MapQuery{Limit: mapPhotoLimit + 1}
	if raw := values.Get("bbox"); raw != "" {
		parts := strings.Split(raw, ",")
		if len(parts) != 4 {
			return q, "bbox must be west,south,east,north"
		}
		var box [4]float64
		for i, part := range parts {
			value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return q, "bbox must be four numbers"
			}
			box[i] = value
		}
		if box[0] < -180 || box[2] > 180 || box[0] > 180 || box[2] < -180 ||
			box[1] < -90 || box[3] > 90 || box[1] > box[3] {
			return q, "bbox is out of range"
		}
		q.BBox = &box
	}
	if raw := values.Get("trip_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return q, "trip_id must be a UUID"
		}
		q.TripID = &id
	}
	for _, field := range []struct {
		name   string
		target **string
	}{{"from", &q.From}, {"to", &q.To}} {
		if raw := values.Get(field.name); raw != "" {
			if !photos.ValidDate(raw) {
				return q, field.name + " must be a date, YYYY-MM-DD"
			}
			value := raw
			*field.target = &value
		}
	}
	q.Text = strings.TrimSpace(values.Get("q"))
	if utf8.RuneCountInString(q.Text) > 100 {
		return q, "q must be at most 100 characters"
	}
	return q, ""
}

// mapPhotos returns the caller's located photos for the owner map (GET
// /api/map/photos): at most 2,000, with truncated set when more matched.
func (h *uploadHandlers) mapPhotos(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	query, message := parseMapQuery(r)
	if message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	records, err := h.overview.MapPhotos(r.Context(), owner, query)
	if err != nil {
		uploadInternalError(w, "map photos", err)
		return
	}
	page := mapPhotoPage{Items: make([]mapPhoto, 0, min(len(records), mapPhotoLimit))}
	if len(records) > mapPhotoLimit {
		records, page.Truncated = records[:mapPhotoLimit], true
	}
	names := make([]string, len(records))
	for i, record := range records {
		if record.DisplayObjectName != nil {
			names[i] = *record.DisplayObjectName
		}
	}
	images, err := h.signDisplays(names)
	if err != nil {
		uploadInternalError(w, "display signing", err)
		return
	}
	for i, record := range records {
		page.Items = append(page.Items, mapPhoto{
			ID: record.ID.String(), TripID: record.TripID.String(), TripTitle: record.TripTitle,
			Lat: *record.Lat, Lng: *record.Lng, DisplayImage: images[i], CaptureTimeLocal: record.CaptureTimeLocal,
			City: record.City, Country: record.Country, PlaceName: record.PlaceName,
		})
	}
	writeJSON(w, http.StatusOK, page)
}

// summary returns the caller's Dashboard totals and recent uploads (GET
// /api/me/summary).
func (h *uploadHandlers) summary(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	s, err := h.overview.Summary(r.Context(), owner, recentUploads)
	if err != nil {
		uploadInternalError(w, "summary", err)
		return
	}
	response := meSummary{
		PhotoCount: s.PhotoCount, CountryCount: s.CountryCount, CityCount: s.CityCount,
		ToReviewCount: s.ToReview, RecentlyUploaded: make([]apiPhoto, 0, len(s.Recent)),
	}
	for _, record := range s.Recent {
		photo, err := h.photoResponse(record)
		if err != nil {
			uploadInternalError(w, "display signing", err)
			return
		}
		response.RecentlyUploaded = append(response.RecentlyUploaded, photo)
	}
	writeJSON(w, http.StatusOK, response)
}
