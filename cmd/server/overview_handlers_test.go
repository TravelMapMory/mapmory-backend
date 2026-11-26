package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/photos"
	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/google/uuid"
)

// overviewFake returns canned aggregates and records the map query it got.
type overviewFake struct {
	owner   uuid.UUID
	stats   map[uuid.UUID]photos.TripStats
	mapRows []photos.MapPhoto
	summary photos.Summary
	query   photos.MapQuery
	err     error
}

func (f *overviewFake) TripStats(_ context.Context, owner uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]photos.TripStats, error) {
	result := make(map[uuid.UUID]photos.TripStats)
	for _, id := range ids {
		if owner == f.owner {
			result[id] = f.stats[id]
		}
	}
	return result, f.err
}

func (f *overviewFake) MapPhotos(_ context.Context, owner uuid.UUID, q photos.MapQuery) ([]photos.MapPhoto, error) {
	f.query = q
	if owner != f.owner {
		return nil, nil
	}
	if len(f.mapRows) > q.Limit {
		return f.mapRows[:q.Limit], f.err
	}
	return f.mapRows, f.err
}

func (f *overviewFake) Summary(_ context.Context, owner uuid.UUID, _ int) (photos.Summary, error) {
	if owner != f.owner {
		return photos.Summary{}, nil
	}
	return f.summary, f.err
}

// singleTripRepository serves one trip to its owner.
type singleTripRepository struct {
	tripRepositoryFake
	trip trips.Trip
}

func (r singleTripRepository) ListByOwner(_ context.Context, owner uuid.UUID) ([]trips.Trip, error) {
	if owner != r.trip.OwnerID {
		return []trips.Trip{}, nil
	}
	return []trips.Trip{r.trip}, nil
}

func (r singleTripRepository) GetByID(_ context.Context, owner, id uuid.UUID) (trips.Trip, error) {
	if owner != r.trip.OwnerID || id != r.trip.ID {
		return trips.Trip{}, trips.ErrNotFound
	}
	return r.trip, nil
}

func newOverviewTestEnv(t *testing.T, overview *overviewFake) (*photoTestEnv, trips.Trip) {
	t.Helper()
	env := newPhotoTestEnv(t)
	overview.owner = env.owner
	record := trips.Trip{ID: env.tripID, OwnerID: env.owner, Title: "Japan 2025", CreatedAt: time.Now()}
	verifier := endpointVerifierFunc(func(_ context.Context, token string) (*firebaseauth.Token, error) {
		if token != "valid" {
			return nil, errors.New("invalid token")
		}
		return &firebaseauth.Token{UID: "subject-a"}, nil
	})
	resolver := endpointResolverFunc(func(context.Context, string) (uuid.UUID, error) { return env.owner, nil })
	repo := singleTripRepository{trip: record}
	uploads := newUploadHandlers(repo, &batchRepositoryFake{}, env.repo, env.store)
	uploads.overview = overview
	env.handler = newPersistentHandler(verifier, resolver, repo, uploads)
	return env, record
}

func located(id uuid.UUID, title string, lat, lng float64) photos.MapPhoto {
	display := "display/" + id.String() + ".jpg"
	city := "Kyoto"
	return photos.MapPhoto{
		Photo:     photos.Photo{ID: id, TripID: uuid.New(), State: photos.StateReady, Lat: &lat, Lng: &lng, DisplayObjectName: &display, City: &city},
		TripTitle: title,
	}
}

func TestTripResponsesIncludePhotoAggregates(t *testing.T) {
	coverID := uuid.New()
	display := "display/cover.jpg"
	start, end := "2025-05-12", "2025-05-14"
	overview := &overviewFake{}
	env, record := newOverviewTestEnv(t, overview)
	overview.stats = map[uuid.UUID]photos.TripStats{record.ID: {
		PhotoCount: 4, LocatedCount: 3, Cities: []string{"Kyoto", "Osaka"}, Countries: []string{"Japan"},
		StartDate: &start, EndDate: &end, Cover: &photos.Photo{ID: coverID, DisplayObjectName: &display},
	}}

	for _, path := range []string{"/api/trips", "/api/trips/" + record.ID.String()} {
		response := tripRequest(env.handler, "GET", path, "", "Bearer valid")
		if !strings.Contains(response.Body.String(), `"visibility":"private"`) {
			t.Fatalf("%s: a trip without a public share must be private: %s", path, response.Body.String())
		}
		if response.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", path, response.Code, response.Body.String())
		}
		body := response.Body.String()
		for _, want := range []string{`"photo_count":4`, `"located_count":3`, `"cities":["Kyoto","Osaka"]`,
			`"countries":["Japan"]`, `"start_date":"2025-05-12"`, `"end_date":"2025-05-14"`, `"photo_id":"` + coverID.String()} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: missing %s in %s", path, want, body)
			}
		}
	}
}

func TestTripResponseWithoutPhotos(t *testing.T) {
	env, record := newOverviewTestEnv(t, &overviewFake{stats: map[uuid.UUID]photos.TripStats{}})
	response := tripRequest(env.handler, "GET", "/api/trips/"+record.ID.String(), "", "Bearer valid")
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"photo_count":0`) || !strings.Contains(body, `"cover":null`) ||
		!strings.Contains(body, `"cities":[]`) || !strings.Contains(body, `"start_date":null`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}

func TestMapPhotosResponse(t *testing.T) {
	first := uuid.New()
	overview := &overviewFake{mapRows: []photos.MapPhoto{located(first, "Japan 2025", 35.01, 135.77)}}
	env, _ := newOverviewTestEnv(t, overview)

	response := tripRequest(env.handler, "GET",
		"/api/map/photos?bbox=130,30,140,40&from=2025-05-01&to=2025-05-31&q=%20kyoto%20", "", "Bearer valid")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page mapPhotoPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Truncated || len(page.Items) != 1 || page.Items[0].ID != first.String() || page.Items[0].TripTitle != "Japan 2025" ||
		page.Items[0].Lat != 35.01 || page.Items[0].DisplayImage == nil || *page.Items[0].City != "Kyoto" {
		t.Fatalf("page = %+v", page)
	}
	q := overview.query
	if q.BBox == nil || q.BBox[2] != 140 || *q.From != "2025-05-01" || *q.To != "2025-05-31" || q.Text != "kyoto" ||
		q.Limit != mapPhotoLimit+1 {
		t.Fatalf("query = %+v", q)
	}
	if strings.Contains(response.Body.String(), "exif_lat") {
		t.Fatal("map markers must not carry EXIF fields")
	}
}

func TestMapPhotosTruncatesAtLimit(t *testing.T) {
	rows := make([]photos.MapPhoto, mapPhotoLimit+1)
	for i := range rows {
		rows[i] = located(uuid.New(), "Big trip", 1, 1)
	}
	env, _ := newOverviewTestEnv(t, &overviewFake{mapRows: rows})
	response := tripRequest(env.handler, "GET", "/api/map/photos", "", "Bearer valid")
	var page mapPhotoPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || len(page.Items) != mapPhotoLimit {
		t.Fatalf("truncated=%v items=%d", page.Truncated, len(page.Items))
	}
}

func TestMapPhotosValidation(t *testing.T) {
	env, _ := newOverviewTestEnv(t, &overviewFake{})
	for _, query := range []string{
		"bbox=1,2,3", "bbox=a,b,c,d", "bbox=-181,0,0,1", "bbox=0,10,1,5", "bbox=0,-91,1,0", "bbox=NaN,0,1,1",
		"trip_id=x", "from=2025-13-01", "to=yesterday", "q=" + strings.Repeat("x", 101),
	} {
		if response := tripRequest(env.handler, "GET", "/api/map/photos?"+query, "", "Bearer valid"); response.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d", query, response.Code)
		}
	}
	// An antimeridian box is valid: west > east.
	if response := tripRequest(env.handler, "GET", "/api/map/photos?bbox=170,-30,-160,0", "", "Bearer valid"); response.Code != http.StatusOK {
		t.Fatalf("antimeridian bbox: status=%d", response.Code)
	}
}

func TestSummaryResponse(t *testing.T) {
	recentID := uuid.New()
	display := "display/recent.jpg"
	overview := &overviewFake{summary: photos.Summary{
		PhotoCount: 12, CountryCount: 2, CityCount: 5, ToReview: 3,
		Recent: []photos.Photo{{ID: recentID, State: photos.StateReady, DisplayObjectName: &display, LabelStatus: photos.LabelSuggested}},
	}}
	env, _ := newOverviewTestEnv(t, overview)
	response := tripRequest(env.handler, "GET", "/api/me/summary", "", "Bearer valid")
	var got meSummary
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got.PhotoCount != 12 || got.CountryCount != 2 || got.CityCount != 5 || got.ToReviewCount != 3 ||
		len(got.RecentlyUploaded) != 1 || got.RecentlyUploaded[0].ID != recentID.String() ||
		got.RecentlyUploaded[0].DisplayImage == nil || got.RecentlyUploaded[0].LabelStatus != photos.LabelSuggested {
		t.Fatalf("summary = %+v", got)
	}
}

func TestOverviewRoutesRequireAuthAndHideErrors(t *testing.T) {
	overview := &overviewFake{err: errors.New("secret sql detail")}
	env, _ := newOverviewTestEnv(t, overview)
	for _, path := range []string{"/api/map/photos", "/api/me/summary"} {
		if response := tripRequest(env.handler, "GET", path, "", ""); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token: status=%d", path, response.Code)
		}
		response := tripRequest(env.handler, "GET", path, "", "Bearer valid")
		if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("%s failure: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestTripVisibilityFollowsPublicShares(t *testing.T) {
	overview := &overviewFake{}
	env, record := newOverviewTestEnv(t, overview)
	overview.stats = map[uuid.UUID]photos.TripStats{record.ID: {Public: true, UnderReview: true}}
	response := tripRequest(env.handler, "GET", "/api/trips/"+record.ID.String(), "", "Bearer valid")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"visibility":"public"`) ||
		!strings.Contains(response.Body.String(), `"under_review":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
