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
	"github.com/TravelMapMory/mapmory-backend/internal/place"
	"github.com/TravelMapMory/mapmory-backend/internal/shares"
	"github.com/google/uuid"
)

// sharesFake is an in-memory shares.Repository for one owner and trip.
type sharesFake struct {
	owner, tripID uuid.UUID
	candidates    map[uuid.UUID]shares.Candidate
	created       []shares.NewShare
	list          []shares.Share
	collection    shares.Collection
	tokenErr      error
	revoked       []uuid.UUID
	points        []shares.PublicPoint
}

func (f *sharesFake) Candidates(_ context.Context, owner, trip uuid.UUID, ids []uuid.UUID) ([]shares.Candidate, error) {
	if owner != f.owner || trip != f.tripID {
		return nil, shares.ErrNotFound
	}
	result := make([]shares.Candidate, 0)
	for _, id := range ids {
		if c, ok := f.candidates[id]; ok {
			result = append(result, c)
		}
	}
	return result, nil
}

func (f *sharesFake) Create(_ context.Context, owner uuid.UUID, s shares.NewShare) (shares.Share, error) {
	if owner != f.owner {
		return shares.Share{}, shares.ErrNotFound
	}
	f.created = append(f.created, s)
	ids := make([]uuid.UUID, len(s.Photos))
	for i, p := range s.Photos {
		ids[i] = p.PhotoID
	}
	return shares.Share{ID: uuid.New(), TripID: s.TripID, Mode: s.Mode, Title: s.Title, Description: s.Description,
		Token: "tokentokentokentokentoken", PhotoIDs: ids, CreatedAt: time.Now()}, nil
}

func (f *sharesFake) ListByTrip(_ context.Context, owner, trip uuid.UUID) ([]shares.Share, error) {
	if owner != f.owner || trip != f.tripID {
		return nil, shares.ErrNotFound
	}
	return f.list, nil
}

func (f *sharesFake) Revoke(_ context.Context, owner, id uuid.UUID) error {
	if owner != f.owner {
		return shares.ErrNotFound
	}
	f.revoked = append(f.revoked, id)
	return nil
}

func (f *sharesFake) ByToken(context.Context, string) (shares.Collection, error) {
	return f.collection, f.tokenErr
}

func (f *sharesFake) PublicByID(context.Context, uuid.UUID) (shares.Collection, error) {
	return f.collection, f.tokenErr
}

func (f *sharesFake) PublicPoints(context.Context, float64, float64, float64, float64, int) ([]shares.PublicPoint, error) {
	return f.points, nil
}

// citySearcher answers every search with Kyoto's centre.
type citySearcher struct{ calls int }

func (s *citySearcher) Search(context.Context, string, int) ([]place.Candidate, error) {
	s.calls++
	return []place.Candidate{{Kind: place.KindCity, City: "Kyoto", Country: "Japan", Lat: 35.0116, Lng: 135.7681}}, nil
}

func newShareTestEnv(t *testing.T, searcher place.Searcher) (*photoTestEnv, *sharesFake) {
	t.Helper()
	env := newPhotoTestEnv(t)
	repo := &sharesFake{owner: env.owner, tripID: env.tripID, candidates: map[uuid.UUID]shares.Candidate{}}
	verifier := endpointVerifierFunc(func(_ context.Context, token string) (*firebaseauth.Token, error) {
		if token != "valid" {
			return nil, errors.New("invalid token")
		}
		return &firebaseauth.Token{UID: "subject-a"}, nil
	})
	resolver := endpointResolverFunc(func(context.Context, string) (uuid.UUID, error) { return env.owner, nil })
	uploads := newUploadHandlers(tripRepositoryFake{}, &batchRepositoryFake{}, env.repo, env.store)
	uploads.shares = newShareHandlers(repo, uploads, searcher)
	env.handler = newPersistentHandler(verifier, resolver, tripRepositoryFake{}, uploads)
	return env, repo
}

func strp(value string) *string { return &value }

func TestCreateShareStoresCoarsePositions(t *testing.T) {
	searcher := &citySearcher{}
	env, repo := newShareTestEnv(t, searcher)
	inKyoto, rounded, unlocated := uuid.New(), uuid.New(), uuid.New()
	lat, lng := 35.04321, 135.70987
	repo.candidates[inKyoto] = shares.Candidate{ID: inKyoto, Lat: &lat, Lng: &lng, City: strp("Kyoto"), Country: strp("Japan")}
	repo.candidates[rounded] = shares.Candidate{ID: rounded, Lat: &lat, Lng: &lng}
	repo.candidates[unlocated] = shares.Candidate{ID: unlocated}

	body := `{"mode":"link","title":"  Kyoto  ","description":" ","photo_ids":["` + inKyoto.String() + `","` +
		rounded.String() + `","` + unlocated.String() + `","` + inKyoto.String() + `"]}`
	response := tripRequest(env.handler, "POST", "/api/trips/"+env.tripID.String()+"/shares", body, "Bearer valid")
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var got apiShare
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "Kyoto" || got.Description != nil || got.Token == "" || len(got.PhotoIDs) != 3 {
		t.Fatalf("share = %+v; title trimmed, blank description dropped, duplicates removed", got)
	}
	photos := repo.created[0].Photos
	if *photos[0].DisplayLat != 35.0116 || *photos[0].DisplayLng != 135.7681 {
		t.Fatalf("city photo at %v,%v; want the city centre", *photos[0].DisplayLat, *photos[0].DisplayLng)
	}
	if *photos[1].DisplayLat != 35.04 || *photos[1].DisplayLng != 135.71 {
		t.Fatalf("unlabelled photo at %v,%v; want 0.01° rounding", *photos[1].DisplayLat, *photos[1].DisplayLng)
	}
	if photos[2].DisplayLat != nil || searcher.calls != 1 {
		t.Fatalf("unlocated=%v searches=%d", photos[2].DisplayLat, searcher.calls)
	}
}

func TestCreateShareValidation(t *testing.T) {
	env, repo := newShareTestEnv(t, nil)
	ok := uuid.New()
	repo.candidates[ok] = shares.Candidate{ID: ok}
	path := "/api/trips/" + env.tripID.String() + "/shares"
	for _, body := range []string{
		``, `{}`, `{"mode":"group","title":"x","photo_ids":["` + ok.String() + `"]}`,
		`{"mode":"link","title":"   ","photo_ids":["` + ok.String() + `"]}`,
		`{"mode":"link","title":"` + strings.Repeat("x", 121) + `","photo_ids":["` + ok.String() + `"]}`,
		`{"mode":"link","title":"x","description":"` + strings.Repeat("d", 601) + `","photo_ids":["` + ok.String() + `"]}`,
		`{"mode":"link","title":"x","photo_ids":[]}`, `{"mode":"link","title":"x","photo_ids":["nope"]}`,
		`{"mode":"link","title":"x","photo_ids":["` + ok.String() + `"],"extra":1}`,
		// A photo that is not a processed photo of this trip.
		`{"mode":"link","title":"x","photo_ids":["` + ok.String() + `","` + uuid.NewString() + `"]}`,
	} {
		if response := tripRequest(env.handler, "POST", path, body, "Bearer valid"); response.Code != http.StatusBadRequest {
			t.Errorf("%.60s: status=%d body=%s", body, response.Code, response.Body.String())
		}
	}
	if len(repo.created) != 0 {
		t.Fatal("an invalid request created a share")
	}
	other := "/api/trips/" + uuid.NewString() + "/shares"
	if response := tripRequest(env.handler, "POST", other, `{"mode":"link","title":"x","photo_ids":["`+ok.String()+`"]}`, "Bearer valid"); response.Code != http.StatusNotFound {
		t.Fatalf("other trip: status=%d", response.Code)
	}
}

func TestOwnerShareRoutesNeedAuth(t *testing.T) {
	env, repo := newShareTestEnv(t, nil)
	tripPath := "/api/trips/" + env.tripID.String() + "/shares"
	for _, request := range [][2]string{{"GET", tripPath}, {"POST", tripPath}, {"DELETE", "/api/shares/" + uuid.NewString()}} {
		if response := tripRequest(env.handler, request[0], request[1], "{}", ""); response.Code != http.StatusUnauthorized {
			t.Fatalf("%v without token: status=%d", request, response.Code)
		}
	}
	shareID := uuid.New()
	repo.list = []shares.Share{{ID: shareID, TripID: env.tripID, Mode: shares.ModeLink, Title: "x", Token: "tok", PhotoIDs: []uuid.UUID{}}}
	if response := tripRequest(env.handler, "GET", tripPath, "", "Bearer valid"); response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), shareID.String()) {
		t.Fatalf("list: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := tripRequest(env.handler, "DELETE", "/api/shares/"+shareID.String(), "", "Bearer valid"); response.Code != http.StatusNoContent ||
		len(repo.revoked) != 1 {
		t.Fatalf("revoke: status=%d", response.Code)
	}
}

func publicRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}

func TestRecipientViewNeedsNoAccountAndHidesExactPositions(t *testing.T) {
	env, repo := newShareTestEnv(t, nil)
	lat, lng := 35.0116, 135.7681
	repo.collection = shares.Collection{Title: "Kyoto", CreatedAt: time.Now(), Photos: []shares.SharedPhoto{{
		ID: uuid.New(), DisplayObjectName: "display/a.jpg", City: strp("Kyoto"), DisplayLat: &lat, DisplayLng: &lng,
	}}}
	token := "abcdefghijklmnopqrstuvwxyz"
	response := publicRequest(env.handler, "/api/s/"+token)
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
	for _, forbidden := range []string{`"lat"`, `"lng"`, "exif", "originals/", "trip_id", "notes"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("recipient response leaks %s: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"display_lat":35.0116`) || !strings.Contains(body, `"display_image":{"url"`) {
		t.Fatalf("body = %s", body)
	}

	repo.tokenErr = shares.ErrRevoked
	if response := publicRequest(env.handler, "/api/s/"+token); response.Code != http.StatusGone ||
		!strings.Contains(response.Body.String(), "share_revoked") {
		t.Fatalf("revoked: status=%d body=%s", response.Code, response.Body.String())
	}
	repo.tokenErr = shares.ErrNotFound
	for _, path := range []string{"/api/s/" + token, "/api/s/short", "/api/public/shares/" + uuid.NewString(), "/api/public/shares/x"} {
		if response := publicRequest(env.handler, path); response.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d", path, response.Code)
		}
	}
}

func TestPublicMap(t *testing.T) {
	env, repo := newShareTestEnv(t, nil)
	share := uuid.New()
	city := "Kyoto"
	repo.points = []shares.PublicPoint{
		{PhotoID: uuid.New(), ShareID: share, ShareTitle: "Kyoto days", DisplayObjectName: "display/a.jpg", Lat: 35.01, Lng: 135.77, City: &city},
		{PhotoID: uuid.New(), ShareID: share, ShareTitle: "Kyoto days", DisplayObjectName: "display/b.jpg", Lat: 35.01, Lng: 135.77, City: &city},
	}
	response := publicRequest(env.handler, "/api/public/map?bbox=130,30,140,40&zoom=6")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page apiPublicMap
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Truncated || len(page.Clusters) != 1 {
		t.Fatalf("page = %+v", page)
	}
	c := page.Clusters[0]
	if c.Count != 2 || c.Label == nil || *c.Label != "Kyoto" || c.CoverURL == nil || c.ShareCount != 1 ||
		c.Shares[0].PhotoCount != 2 || c.Shares[0].CoverURL == nil {
		t.Fatalf("cluster = %+v", c)
	}
	for _, query := range []string{"", "bbox=1,2,3&zoom=4", "bbox=0,0,1,1", "bbox=0,0,1,1&zoom=23", "bbox=0,10,1,5&zoom=3", "bbox=0,0,200,1&zoom=3"} {
		if response := publicRequest(env.handler, "/api/public/map?"+query); response.Code != http.StatusBadRequest {
			t.Errorf("%q: status=%d", query, response.Code)
		}
	}
}
