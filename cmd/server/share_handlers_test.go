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
	onCandidates  func()
	onCreate      func(context.Context)
	publicLimit   int
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
	if f.onCandidates != nil {
		f.onCandidates()
	}
	return result, nil
}

func (f *sharesFake) Create(ctx context.Context, owner uuid.UUID, s shares.NewShare) (shares.Share, error) {
	if owner != f.owner {
		return shares.Share{}, shares.ErrNotFound
	}
	if f.onCreate != nil {
		f.onCreate(ctx)
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

func (f *sharesFake) PublicPoints(_ context.Context, _, _, _, _ float64, limit int) ([]shares.PublicPoint, error) {
	f.publicLimit = limit
	if len(f.points) > limit {
		return f.points[:limit], nil
	}
	return f.points, nil
}

// citySearcher answers every search with Kyoto's centre.
type citySearcher struct{ calls int }

func (s *citySearcher) Search(context.Context, string, int) ([]place.Candidate, error) {
	s.calls++
	return []place.Candidate{{Kind: place.KindCity, City: "Kyoto", Country: "Japan", Lat: 35.0116, Lng: 135.7681}}, nil
}

// callbackCitySearcher permits deterministic deadline/cancellation testing without sleeping.
type callbackCitySearcher struct {
	calls  int
	search func(context.Context, string) ([]place.Candidate, error)
}

func (s *callbackCitySearcher) Search(ctx context.Context, query string, _ int) ([]place.Candidate, error) {
	s.calls++
	return s.search(ctx, query)
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
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
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

// Cancel the request after candidate selection, immediately before the write.
// The committed share must get a fresh deadline instead of inheriting cancellation.
func TestCreateShareUsesBoundedWriteContext(t *testing.T) {
	env, repo := newShareTestEnv(t, nil)
	id := uuid.New()
	repo.candidates[id] = shares.Candidate{ID: id}

	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo.onCandidates = cancel

	called := false
	repo.onCreate = func(ctx context.Context) {
		called = true
		if err := ctx.Err(); err != nil {
			t.Errorf("Create context already canceled: %v", err)
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("Create context has no deadline")
			return
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > shareWriteTimeout {
			t.Errorf("Create deadline remaining = %v; want (0, %v]", remaining, shareWriteTimeout)
		}
	}

	body := `{"mode":"link","title":"Kyoto","photo_ids":["` + id.String() + `"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/trips/"+env.tripID.String()+"/shares", strings.NewReader(body)).WithContext(requestCtx)
	req.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	env.handler.ServeHTTP(response, req)
	if !called || response.Code != http.StatusCreated {
		t.Fatalf("Create called=%v status=%d body=%s", called, response.Code, response.Body.String())
	}
}

// The lookup budget applies across all photos, rather than resetting for each
// distinct city. A canceled lookup must not prevent the detached database write.
func TestCreateShareLookupCancellationFallsBackToRoundedCoordinates(t *testing.T) {
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	searcher := &callbackCitySearcher{}
	searcher.search = func(ctx context.Context, _ string) ([]place.Candidate, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("place lookup has no deadline")
		} else if remaining := time.Until(deadline); remaining <= 0 || remaining > shareLookupBudget {
			t.Errorf("lookup deadline remaining = %v; want (0, %v]", remaining, shareLookupBudget)
		}
		// Simulate a disconnected client during the first provider lookup.
		cancel()
		return nil, ctx.Err()
	}
	env, repo := newShareTestEnv(t, searcher)
	lat, lng := 35.04321, 135.70987
	first, second := uuid.New(), uuid.New()
	repo.candidates[first] = shares.Candidate{ID: first, Lat: &lat, Lng: &lng, City: strp("Kyoto"), Country: strp("Japan")}
	repo.candidates[second] = shares.Candidate{ID: second, Lat: &lat, Lng: &lng, City: strp("Osaka"), Country: strp("Japan")}

	body := `{"mode":"link","title":"Japan","photo_ids":["` + first.String() + `","` + second.String() + `"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/trips/"+env.tripID.String()+"/shares", strings.NewReader(body)).WithContext(requestCtx)
	req.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	env.handler.ServeHTTP(response, req)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if searcher.calls != 1 {
		t.Errorf("provider searches=%d; want 1 after cancellation", searcher.calls)
	}
	if len(repo.created) != 1 || len(repo.created[0].Photos) != 2 {
		t.Fatalf("shares created = %+v", repo.created)
	}
	for _, photo := range repo.created[0].Photos {
		if photo.DisplayLat == nil || photo.DisplayLng == nil || *photo.DisplayLat != 35.04 || *photo.DisplayLng != 135.71 {
			t.Errorf("fallback position = %v,%v; want 35.04,135.71", photo.DisplayLat, photo.DisplayLng)
		}
	}
}

// Verify the input-row cap separately from the 200-cluster response cap.
// In particular, overflow must be reported even when all points form one cell.
func TestPublicMapTruncationAtRepositoryLimit(t *testing.T) {
	const path = "/api/public/map?bbox=130,30,140,40&zoom=6"
	for _, tc := range []struct {
		name      string
		count     int
		truncated bool
	}{
		{name: "below cap", count: 2, truncated: false},
		{name: "exact cap", count: publicPointLimit, truncated: false},
		{name: "over cap", count: publicPointLimit + 1, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, repo := newShareTestEnv(t, nil)
			sharedID := uuid.New()
			repo.points = make([]shares.PublicPoint, tc.count)
			for i := range repo.points {
				repo.points[i] = shares.PublicPoint{
					PhotoID: uuid.New(), ShareID: sharedID, ShareTitle: "Kyoto",
					Lat: 35.01, Lng: 135.77,
				}
			}
			response := publicRequest(env.handler, path)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var result apiPublicMap
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if repo.publicLimit != publicPointLimit+1 {
				t.Errorf("repository limit=%d; want %d", repo.publicLimit, publicPointLimit+1)
			}
			if result.Truncated != tc.truncated {
				t.Errorf("truncated=%v; want %v", result.Truncated, tc.truncated)
			}
			if len(result.Clusters) != 1 || result.Clusters[0].Count != min(tc.count, publicPointLimit) {
				t.Errorf("clusters=%+v; want one cluster of %d photos", result.Clusters, min(tc.count, publicPointLimit))
			}
		})
	}
}
