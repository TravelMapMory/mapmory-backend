package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/photos"
	"github.com/TravelMapMory/mapmory-backend/internal/place"
	"github.com/google/uuid"
)

// providerFake answers reverse geocoding with a fixed place or error and
// records how often it was asked.
type providerFake struct {
	mu     sync.Mutex
	result place.ResolvedPlace
	err    error
	calls  int
}

func (p *providerFake) ReverseGeocode(context.Context, float64, float64) (place.ResolvedPlace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.result, p.err
}

func (p *providerFake) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

var kyoto = place.ResolvedPlace{
	Country: "Japan", City: "Kyoto", Name: "Fushimi Inari Taisha", Provider: "geoapify",
	Attributions: []place.Attribution{{Text: "Powered by Geoapify"}, {Text: "© OpenStreetMap contributors"}},
}

// newLocationTestEnv is newPhotoTestEnv with a place resolver backed by provider.
func newLocationTestEnv(t *testing.T, provider *providerFake) *photoTestEnv {
	t.Helper()
	env := newPhotoTestEnv(t)
	verifier := endpointVerifierFunc(func(_ context.Context, token string) (*firebaseauth.Token, error) {
		if token != "valid" {
			return nil, errors.New("invalid token")
		}
		return &firebaseauth.Token{UID: "subject-a"}, nil
	})
	resolver := endpointResolverFunc(func(context.Context, string) (uuid.UUID, error) { return env.owner, nil })
	uploads := newUploadHandlers(tripRepositoryFake{}, &batchRepositoryFake{}, env.repo, env.store)
	if provider != nil {
		uploads.places = place.NewResolver(provider)
	}
	env.handler = newPersistentHandler(verifier, resolver, tripRepositoryFake{}, uploads)
	return env
}

// processed adds a photo in state with an optional EXIF position.
func (env *photoTestEnv) processed(state string, lat, lng *float64) photos.Photo {
	photo := env.repo.add(env.batchID, uuid.NewString(), 100)
	env.repo.mu.Lock()
	defer env.repo.mu.Unlock()
	stored := env.repo.photos[photo.ID]
	display := "display/" + photo.ID.String() + ".jpg"
	stored.State, stored.DisplayObjectName = state, &display
	stored.ExifLat, stored.ExifLng, stored.Lat, stored.Lng = lat, lng, lat, lng
	stored.LabelStatus = photos.LabelPending
	return *stored
}

func floatPtr(value float64) *float64 { return &value }

func (env *photoTestEnv) patchLocation(id uuid.UUID, body, token string) (*apiPhoto, *httptest.ResponseRecorder) {
	response := tripRequest(env.handler, "PATCH", "/api/photos/"+id.String()+"/location", body, token)
	if response.Code != http.StatusOK {
		return nil, response
	}
	var photo apiPhoto
	if err := json.Unmarshal(response.Body.Bytes(), &photo); err != nil {
		panic(err)
	}
	return &photo, response
}

func TestCompleteUploadSuggestsLabelsForGPSPhoto(t *testing.T) {
	provider := &providerFake{result: kyoto}
	env := newLocationTestEnv(t, provider)
	env.registerUploaded("a", readFixture(t, "gps.jpg"))

	got, status, body := env.complete("a")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if got.LabelStatus != photos.LabelSuggested || got.City == nil || *got.City != "Kyoto" ||
		got.Country == nil || *got.Country != "Japan" || got.PlaceName == nil {
		t.Fatalf("labels = %+v", got)
	}
	if got.LabelAttribution == nil || !strings.Contains(*got.LabelAttribution, "OpenStreetMap") {
		t.Fatalf("attribution = %v", got.LabelAttribution)
	}
	if strings.Contains(body, "label_provider") {
		t.Fatalf("internal provider field leaked: %s", body)
	}
}

func TestCompleteUploadKeepsPhotoWhenProviderFails(t *testing.T) {
	provider := &providerFake{err: place.ErrTransient}
	env := newLocationTestEnv(t, provider)
	env.registerUploaded("a", readFixture(t, "gps.jpg"))

	got, status, body := env.complete("a")
	if status != http.StatusOK || got.State != photos.StateReady {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if got.LabelStatus != photos.LabelPending || got.City != nil {
		t.Fatalf("labels must stay pending: %+v", got)
	}
}

func TestCompleteUploadWithoutGPSSkipsProvider(t *testing.T) {
	provider := &providerFake{result: kyoto}
	env := newLocationTestEnv(t, provider)
	env.registerUploaded("a", readFixture(t, "no_exif.jpg"))

	got, status, body := env.complete("a")
	if status != http.StatusOK || got.State != photos.StateNeedsLocation || got.LabelStatus != photos.LabelPending {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if provider.callCount() != 0 {
		t.Fatalf("provider called %d times for a photo without GPS", provider.callCount())
	}
}

func TestUpdatePhotoLocationCorrectsAndConfirms(t *testing.T) {
	provider := &providerFake{result: kyoto}
	env := newLocationTestEnv(t, provider)
	photo := env.processed(photos.StateNeedsLocation, nil, nil)

	got, response := env.patchLocation(photo.ID, `{"lat":34.967,"lng":135.773}`, "Bearer valid")
	if got == nil {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got.State != photos.StateReady || got.Lat == nil || *got.Lat != 34.967 || *got.Lng != 135.773 {
		t.Fatalf("position = %+v", got)
	}
	if got.ExifLat != nil || got.ExifLng != nil {
		t.Fatalf("EXIF pair must not change: %v %v", got.ExifLat, got.ExifLng)
	}
	if got.LabelStatus != photos.LabelConfirmed || got.City == nil || *got.City != "Kyoto" {
		t.Fatalf("labels = %+v", got)
	}
}

func TestUpdatePhotoLocationKeepsExifAndCaptureTime(t *testing.T) {
	env := newLocationTestEnv(t, &providerFake{result: kyoto})
	photo := env.processed(photos.StateReady, floatPtr(60.18), floatPtr(24.93))
	capture := "2025-07-14T09:30:00"
	env.repo.mu.Lock()
	env.repo.photos[photo.ID].CaptureTimeLocal = &capture
	env.repo.mu.Unlock()

	got, response := env.patchLocation(photo.ID, `{"lat":34.967,"lng":135.773}`, "Bearer valid")
	if got == nil {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if *got.ExifLat != 60.18 || *got.ExifLng != 24.93 || *got.Lat != 34.967 {
		t.Fatalf("exif=%v,%v lat=%v", *got.ExifLat, *got.ExifLng, *got.Lat)
	}
	if got.CaptureTimeLocal == nil || *got.CaptureTimeLocal != capture {
		t.Fatalf("capture time changed: %v", got.CaptureTimeLocal)
	}
}

func TestUpdatePhotoLocationSavesPositionWhenProviderFails(t *testing.T) {
	env := newLocationTestEnv(t, &providerFake{err: place.ErrTransient})
	photo := env.processed(photos.StateNeedsLocation, nil, nil)

	got, response := env.patchLocation(photo.ID, `{"lat":1.5,"lng":2.5}`, "Bearer valid")
	if got == nil {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got.State != photos.StateReady || *got.Lat != 1.5 || got.LabelStatus != photos.LabelPending || got.City != nil {
		t.Fatalf("photo = %+v", got)
	}
}

func TestUpdatePhotoLocationWithoutResolver(t *testing.T) {
	env := newLocationTestEnv(t, nil)
	photo := env.processed(photos.StateNeedsLocation, nil, nil)

	got, response := env.patchLocation(photo.ID, `{"lat":1.5,"lng":2.5}`, "Bearer valid")
	if got == nil || got.State != photos.StateReady || got.LabelStatus != photos.LabelPending {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestUpdatePhotoLocationUsesBoundedWriteContext(t *testing.T) {
	env := newLocationTestEnv(t, nil)
	photo := env.processed(photos.StateNeedsLocation, nil, nil)

	var (
		writeDeadline time.Time
		writeErr      error
		called        bool
	)

	env.repo.onRelocate = func(ctx context.Context) {
		called = true

		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("Relocate context has no deadline")
			return
		}

		writeDeadline = deadline
		writeErr = ctx.Err()
	}

	// Simulate a client disconnect before the database write.
	// The write should still receive a usable, bounded context.
	requestCtx, cancel := context.WithCancel(context.Background())

	req := httptest.NewRequest(
		http.MethodPatch,
		"/api/photos/"+photo.ID.String()+"/location",
		strings.NewReader(`{"lat":34.967,"lng":135.773}`),
	).WithContext(requestCtx)

	req.Header.Set("Authorization", "Bearer valid")
	cancel()

	response := httptest.NewRecorder()

	env.handler.ServeHTTP(response, req)

	if !called {
		t.Fatal("Relocate was not called")
	}
	if writeErr != nil {
		t.Errorf("write context already canceled: %v", writeErr)
	}

	if !writeDeadline.IsZero() {
		remaining := time.Until(writeDeadline)

		if remaining <= 0 || remaining > locationWriteTimeout {
			t.Errorf(
				"write deadline remaining = %v; expected (0, %v]",
				remaining,
				locationWriteTimeout,
			)
		}
	}

	if response.Code != http.StatusOK {
		t.Errorf(
			"status = %d, want 200; body = %s",
			response.Code,
			response.Body.String(),
		)
	}
}

func TestConfirmSuggestedLabels(t *testing.T) {
	env := newLocationTestEnv(t, &providerFake{result: kyoto})
	photo := env.processed(photos.StateReady, floatPtr(34.967), floatPtr(135.773))

	// Nothing suggested yet: a conflict, not a silent success.
	if _, response := env.patchLocation(photo.ID, `{"confirm_label":true}`, "Bearer valid"); response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "no_suggested_labels") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	if stored, _ := env.repo.SuggestLabels(context.Background(), photo.ID, 34.967, 135.773, labelsFrom(kyoto)); !stored {
		t.Fatal("labels not suggested")
	}
	got, response := env.patchLocation(photo.ID, `{"confirm_label":true}`, "Bearer valid")
	if got == nil || got.LabelStatus != photos.LabelConfirmed || *got.City != "Kyoto" || *got.Lat != 34.967 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	// A confirmed choice is never overwritten by a later suggestion.
	if stored, _ := env.repo.SuggestLabels(context.Background(), photo.ID, 34.967, 135.773, photos.Labels{City: "Other"}); stored {
		t.Fatal("a suggestion overwrote confirmed labels")
	}
}

func TestUpdatePhotoLocationRejectsInvalidBodies(t *testing.T) {
	env := newLocationTestEnv(t, &providerFake{result: kyoto})
	photo := env.processed(photos.StateNeedsLocation, nil, nil)
	for _, body := range []string{
		``, `{}`, `{"lat":1}`, `{"lng":1}`, `{"lat":91,"lng":0}`, `{"lat":0,"lng":-181}`,
		`{"lat":"1","lng":2}`, `{"lat":1,"lng":2,"extra":true}`, `{"lat":1,"lng":2,"confirm_label":true}`,
		`{"confirm_label":false}`, `{"lat":1,"lng":2}{}`, `[]`,
	} {
		if _, response := env.patchLocation(photo.ID, body, "Bearer valid"); response.Code != http.StatusBadRequest {
			t.Errorf("%q: status=%d body=%s", body, response.Code, response.Body.String())
		}
	}
	if env.repo.get(photo.ID).State != photos.StateNeedsLocation {
		t.Fatal("an invalid body changed the photo")
	}
}

func TestUpdatePhotoLocationAccessAndState(t *testing.T) {
	provider := &providerFake{result: kyoto}
	env := newLocationTestEnv(t, provider)
	pending := env.repo.add(env.batchID, "pending", 100)
	body := `{"lat":1,"lng":2}`

	if _, response := env.patchLocation(pending.ID, body, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d", response.Code)
	}
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		response := tripRequest(env.handler, "PATCH", "/api/photos/"+id+"/location", body, "Bearer valid")
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d", id, response.Code)
		}
	}
	if _, response := env.patchLocation(pending.ID, body, "Bearer valid"); response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "photo_not_processed") {
		t.Fatalf("pending photo: status=%d body=%s", response.Code, response.Body.String())
	}
	if provider.callCount() != 0 {
		t.Fatalf("provider called %d times for refused requests", provider.callCount())
	}
}

// searcherFake returns fixed candidates or an error.
type searcherFake struct {
	candidates []place.Candidate
	err        error
	query      string
}

func (s *searcherFake) Search(_ context.Context, query string, _ int) ([]place.Candidate, error) {
	s.query = query
	return s.candidates, s.err
}

func placeSearch(handler http.Handler, query string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/places/search?q="+query, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestPlaceSearch(t *testing.T) {
	searcher := &searcherFake{candidates: []place.Candidate{
		{ID: "c1", Name: "Kyoto", Label: "Kyoto, Japan", Kind: place.KindCity, Lat: 35.01, Lng: 135.77, City: "Kyoto", Country: "Japan"},
		{ID: "c2", Name: "Japan", Label: "Japan", Kind: place.KindCountry, Lat: 36.5, Lng: 139, Country: "Japan"},
		{ID: "l1", Name: "Otaniemi", Label: "Otaniemi, Espoo, Finland", Kind: place.KindLocality, Lat: 60.18, Lng: 24.83, City: "Espoo", Country: "Finland"},
		{ID: "s1", Name: "Rautatientori", Label: "Rautatientori, Helsinki, Finland", Kind: place.KindStreet, Lat: 60.17, Lng: 24.94, City: "Helsinki", Country: "Finland"},
		{ID: "a1", Name: "Taproom d20", Label: "Taproom d20, Helsinki, Finland", Kind: place.KindAmenity, Lat: 60.17, Lng: 24.94, City: "Helsinki", Country: "Finland"},
	}}
	handler := withPlaceSearch(http.NotFoundHandler(), searcher)

	response := placeSearch(handler, "%20Kyoto%20")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if searcher.query != "Kyoto" {
		t.Fatalf("query = %q, want trimmed", searcher.query)
	}
	var got placeSearchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 5 || got.Items[0].Zoom != 11 || got.Items[0].City == nil || *got.Items[0].City != "Kyoto" ||
		got.Items[1].Zoom != 5 || got.Items[1].City != nil || got.Items[1].Kind != "country" ||
		got.Items[2].Zoom != 14 || got.Items[2].City == nil || *got.Items[2].City != "Espoo" ||
		got.Items[3].Zoom != 16 || got.Items[4].Zoom != 17 {
		t.Fatalf("items = %+v", got.Items)
	}
	// No public route requires a token, and other paths fall through.
	if response := placeSearch(handler, "K"); response.Code != http.StatusBadRequest {
		t.Fatalf("short query: status=%d", response.Code)
	}
	if response := placeSearch(handler, strings.Repeat("k", 101)); response.Code != http.StatusBadRequest {
		t.Fatalf("long query: status=%d", response.Code)
	}
}

func TestPlaceSearchEmptyAndUnavailable(t *testing.T) {
	empty := placeSearch(withPlaceSearch(http.NotFoundHandler(), &searcherFake{}), "Nowhere")
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Fatalf("no match: status=%d body=%s", empty.Code, empty.Body.String())
	}
	// A provider failure is an error, never an empty result list.
	failed := placeSearch(withPlaceSearch(http.NotFoundHandler(), &searcherFake{err: place.ErrTransient}), "Kyoto")
	if failed.Code != http.StatusServiceUnavailable || !strings.Contains(failed.Body.String(), "places_unavailable") {
		t.Fatalf("provider failure: status=%d body=%s", failed.Code, failed.Body.String())
	}
	unconfigured := placeSearch(withPlaceSearch(http.NotFoundHandler(), nil), "Kyoto")
	if unconfigured.Code != http.StatusServiceUnavailable {
		t.Fatalf("no key: status=%d", unconfigured.Code)
	}
}

// An aborted browser query is expected when the user continues typing.
// The handler must not attempt to write a 503 after the client has gone away.
func TestPlaceSearchClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/places/search?q=Otaniemi", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	withPlaceSearch(http.NotFoundHandler(), &searcherFake{err: context.Canceled}).ServeHTTP(w, r)
	if w.Body.Len() != 0 {
		t.Fatalf("canceled request wrote a response: %s", w.Body.String())
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	limiter := newRateLimiter(2, 2)
	limiter.now = func() time.Time { return now }
	if !limiter.allow() || !limiter.allow() {
		t.Fatal("burst refused")
	}
	if limiter.allow() {
		t.Fatal("third request in the same instant allowed")
	}
	now = now.Add(500 * time.Millisecond)
	if !limiter.allow() || limiter.allow() {
		t.Fatal("refill after 0.5 s must allow exactly one more")
	}
}

func listFirstPage(env *photoTestEnv) *apiPhoto {
	response := tripRequest(env.handler, "GET", "/api/trips/"+env.tripID.String()+"/photos", "", "Bearer valid")
	var page photoPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Items) == 0 {
		panic(response.Body.String())
	}
	return &page.Items[0]
}

func TestOpeningATripRetriesPendingLabels(t *testing.T) {
	provider := &providerFake{err: place.ErrTransient}
	env := newLocationTestEnv(t, provider)
	env.processed(photos.StateReady, floatPtr(34.967), floatPtr(135.773))

	// The provider is still down: labels stay pending, the listing succeeds.
	if got := listFirstPage(env); got.LabelStatus != photos.LabelPending {
		t.Fatalf("labels = %+v", got)
	}
	// It recovers: the next opening labels the photo.
	provider.mu.Lock()
	provider.err, provider.result = nil, kyoto
	provider.mu.Unlock()
	if got := listFirstPage(env); got.LabelStatus != photos.LabelSuggested || got.City == nil || *got.City != "Kyoto" {
		t.Fatalf("after recovery = %+v", got)
	}
	if provider.callCount() != 2 {
		t.Fatalf("provider calls = %d", provider.callCount())
	}
}

func TestLabelRetriesStopAfterThree(t *testing.T) {
	provider := &providerFake{err: place.ErrNoResult}
	env := newLocationTestEnv(t, provider)
	env.processed(photos.StateReady, floatPtr(0.5), floatPtr(-160.5))
	for i := 0; i < 6; i++ {
		listFirstPage(env)
	}
	if provider.callCount() != photos.MaxLabelRetries {
		t.Fatalf("provider calls = %d, want %d", provider.callCount(), photos.MaxLabelRetries)
	}
}

func TestLabelRetriesSkipLaterPagesAndUnlocatedPhotos(t *testing.T) {
	provider := &providerFake{result: kyoto}
	env := newLocationTestEnv(t, provider)
	env.processed(photos.StateNeedsLocation, nil, nil)
	listFirstPage(env)
	if provider.callCount() != 0 {
		t.Fatal("a photo without a position was sent to the provider")
	}
	env.processed(photos.StateReady, floatPtr(1), floatPtr(2))
	cursor := photos.Cursor{ID: uuid.New()}.Encode()
	tripRequest(env.handler, "GET", "/api/trips/"+env.tripID.String()+"/photos?cursor="+cursor, "", "Bearer valid")
	if provider.callCount() != 0 {
		t.Fatal("a later page retried labels")
	}
}

// Search-selected labels take precedence over a reverse-geocoder's nearby venue.
func TestUpdatePhotoLocationPreservesSelectedPlace(t *testing.T) {
	provider := &providerFake{result: place.ResolvedPlace{
		Country: "Finland", City: "Espoo", Name: "Another nearby venue", Provider: "geoapify",
	}}
	env := newLocationTestEnv(t, provider)
	photo := env.processed(photos.StateNeedsLocation, nil, nil)
	body := `{"lat":60.184,"lng":24.828,"selected_place":{"name":"Täffä","kind":"amenity","city":"","country":""}}`
	got, response := env.patchLocation(photo.ID, body, "Bearer valid")
	if got == nil {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got.PlaceName == nil || *got.PlaceName != "Täffä" || got.City == nil || *got.City != "Espoo" || got.LabelStatus != photos.LabelConfirmed {
		t.Fatalf("selected place not retained: %+v", got)
	}
	if provider.callCount() != 1 {
		t.Fatalf("expected reverse lookup for missing administrative labels, calls=%d", provider.callCount())
	}
	stored := env.repo.get(photo.ID)
	if stored.PlaceName == nil || *stored.PlaceName != "Täffä" {
		t.Fatalf("selected place was not persisted: %+v", stored)
	}
}

func TestUpdatePhotoLocationSelectedCityAndProviderFailure(t *testing.T) {
	env := newLocationTestEnv(t, &providerFake{err: place.ErrTransient})
	photo := env.processed(photos.StateNeedsLocation, nil, nil)
	body := `{"lat":60.2,"lng":24.66,"selected_place":{"name":"Espoo","kind":"city","city":"Espoo","country":"Finland"}}`
	got, response := env.patchLocation(photo.ID, body, "Bearer valid")
	if got == nil || got.City == nil || *got.City != "Espoo" || got.Country == nil || *got.Country != "Finland" || got.PlaceName != nil || got.LabelStatus != photos.LabelConfirmed {
		t.Fatalf("status=%d body=%s photo=%+v", response.Code, response.Body.String(), got)
	}
}

func TestUpdatePhotoLocationRejectsInvalidSelectedPlace(t *testing.T) {
	env := newLocationTestEnv(t, &providerFake{result: kyoto})
	photo := env.processed(photos.StateNeedsLocation, nil, nil)
	for _, body := range []string{
		`{"lat":1,"lng":2,"selected_place":{"name":"","kind":"amenity"}}`,
		`{"lat":1,"lng":2,"selected_place":{"name":"Fake","kind":"bogus"}}`,
		`{"selected_place":{"name":"Fake","kind":"amenity"}}`,
		`{"confirm_label":true,"selected_place":{"name":"Fake","kind":"amenity"}}`,
		`{"lat":1,"lng":2,"selected_place":{"name":"Fake","kind":"amenity","extra":1}}`,
	} {
		if _, response := env.patchLocation(photo.ID, body, "Bearer valid"); response.Code != http.StatusBadRequest {
			t.Errorf("%q: status=%d body=%s", body, response.Code, response.Body.String())
		}
	}
}
