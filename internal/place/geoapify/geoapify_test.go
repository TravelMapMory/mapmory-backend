package geoapify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TravelMapMory/mapmory-backend/internal/place"
)

// testKey is the fake API key used by every test; error handlers echo it so a
// leak into an error message fails the test.
const testKey = "test-key-0123456789"

// whiteCubeBody is a reverse-geocoding reply in the shape Geoapify documents
// for format=json.
const whiteCubeBody = `{"results":[{
	"datasource":{"sourcename":"openstreetmap","attribution":"© OpenStreetMap contributors","license":"Open Database License","url":"https://www.openstreetmap.org/copyright"},
	"country":"United Kingdom","country_code":"gb","state":"England","city":"London",
	"name":"White Cube","lon":-0.08232898,"lat":51.49945965,
	"formatted":"White Cube, 144-152 Bermondsey Street, London, SE1 3TQ, United Kingdom",
	"result_type":"amenity","distance":0}]}`

// newTestClient starts an httptest server running h and returns a Client that
// talks to it. A zero timeout selects the default.
func newTestClient(t *testing.T, h http.HandlerFunc, timeout time.Duration) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{APIKey: testKey, BaseURL: srv.URL, Timeout: timeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// checkErr fails the test unless err matches want and does not contain the
// API key.
func checkErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error leaks the API key: %v", err)
	}
}

// TestNewValidatesConfig checks that a missing key or an unusable base URL is
// rejected and a valid configuration is accepted.
func TestNewValidatesConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"empty key", Config{}, true},
		{"blank key", Config{APIKey: "  "}, true},
		{"ftp base URL", Config{APIKey: testKey, BaseURL: "ftp://x"}, true},
		{"base URL without scheme", Config{APIKey: testKey, BaseURL: "not a url"}, true},
		{"base URL without host", Config{APIKey: testKey, BaseURL: "https://"}, true},
		{"defaults", Config{APIKey: testKey}, false},
		{"explicit", Config{APIKey: testKey, BaseURL: "http://localhost:1234", Timeout: time.Second}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), testKey) {
				t.Fatalf("error leaks the API key: %v", err)
			}
		})
	}
}

// TestReverseGeocodeSuccess checks the outgoing request and the mapping of a
// full reply, including both required credits.
func TestReverseGeocodeSuccess(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/v1/geocode/reverse" {
			t.Errorf("request = %s %s, want GET /v1/geocode/reverse", r.Method, r.URL.Path)
		}
		for key, want := range map[string]string{
			"lat":    "51.49945965",
			"lon":    "-0.08232898",
			"format": "json",
			"apiKey": testKey,
		} {
			if got := q.Get(key); got != want {
				t.Errorf("query %s = %q, want %q", key, got, want)
			}
		}
		fmt.Fprint(w, whiteCubeBody)
	}, 0)

	got, err := c.ReverseGeocode(context.Background(), 51.49945965, -0.08232898)
	if err != nil {
		t.Fatalf("ReverseGeocode: %v", err)
	}

	want := place.ResolvedPlace{
		Country:  "United Kingdom",
		City:     "London",
		Name:     "White Cube",
		Provider: "geoapify",
		Attributions: []place.Attribution{
			{Text: "Powered by Geoapify", URL: "https://www.geoapify.com/"},
			{Text: "© OpenStreetMap contributors", URL: "https://www.openstreetmap.org/copyright"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestReverseGeocodePartial checks that a match with only a country maps to
// empty city and name, and that the Geoapify and OpenStreetMap credits are
// still attached when the reply names no data source.
func TestReverseGeocodePartial(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"country":"Finland"}]}`)
	}, 0)

	got, err := c.ReverseGeocode(context.Background(), 64, 26)
	if err != nil {
		t.Fatalf("ReverseGeocode: %v", err)
	}
	if got.Country != "Finland" || got.City != "" || got.Name != "" {
		t.Fatalf("got %+v, want Country only", got)
	}
	wantCredit := place.Attribution{Text: "© OpenStreetMap contributors", URL: "https://www.openstreetmap.org/copyright"}
	if len(got.Attributions) != 2 || got.Attributions[1] != wantCredit {
		t.Fatalf("attributions = %+v, want the OpenStreetMap credit second", got.Attributions)
	}
}

// TestReverseGeocodeNonOSMSource checks that a result from a non-OpenStreetMap
// data source keeps the Geoapify and OpenStreetMap credits and adds the
// source's own credit third.
func TestReverseGeocodeNonOSMSource(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"country":"Finland","city":"Helsinki","datasource":{"sourcename":"whosonfirst","attribution":"Who's On First","url":"https://whosonfirst.org/docs/licenses/"}}]}`)
	}, 0)

	got, err := c.ReverseGeocode(context.Background(), 60.17, 24.94)
	if err != nil {
		t.Fatalf("ReverseGeocode: %v", err)
	}
	want := []place.Attribution{
		{Text: "Powered by Geoapify", URL: "https://www.geoapify.com/"},
		{Text: "© OpenStreetMap contributors", URL: "https://www.openstreetmap.org/copyright"},
		{Text: "Who's On First", URL: "https://whosonfirst.org/docs/licenses/"},
	}
	if !reflect.DeepEqual(got.Attributions, want) {
		t.Fatalf("attributions = %+v, want %+v", got.Attributions, want)
	}
}

// TestReverseGeocodeErrors checks how each status code and body shape is
// classified, and that error bodies which echo the key never leak it.
func TestReverseGeocodeErrors(t *testing.T) {
	echo := `{"statusCode":0,"message":"Invalid apiKey ` + testKey + `"}`
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"empty results", 200, `{"results":[]}`, place.ErrNoResult},
		{"no results field", 200, `{}`, place.ErrNoResult},
		{"truncated JSON", 200, `{"results":[ "` + testKey, place.ErrPermanent},
		{"wrong JSON type", 200, `{"results":"` + testKey + `"}`, place.ErrPermanent},
		{"bad request", 400, echo, place.ErrPermanent},
		{"unauthorized", 401, echo, place.ErrPermanent},
		{"forbidden", 403, echo, place.ErrPermanent},
		{"request timeout", 408, echo, place.ErrTransient},
		{"rate limited", 429, echo, place.ErrTransient},
		{"server error", 500, echo, place.ErrTransient},
		{"unavailable", 503, echo, place.ErrTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}, 0)
			_, err := c.ReverseGeocode(context.Background(), 60.17, 24.94)
			checkErr(t, err, tt.want)
		})
	}
}

// TestReverseGeocodeTimeout checks that a server slower than the configured
// timeout is a transient failure.
func TestReverseGeocodeTimeout(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}, 50*time.Millisecond)

	_, err := c.ReverseGeocode(context.Background(), 60.17, 24.94)
	checkErr(t, err, place.ErrTransient)
}

// TestReverseGeocodeContextCanceled checks that a canceled context is
// transient and still matches context.Canceled.
func TestReverseGeocodeContextCanceled(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, whiteCubeBody)
	}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.ReverseGeocode(ctx, 60.17, 24.94)
	checkErr(t, err, place.ErrTransient)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to match context.Canceled", err)
	}
}

// TestReverseGeocodeNetworkError checks that a refused connection is a
// transient failure.
func TestReverseGeocodeNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	baseURL := srv.URL
	srv.Close()
	c, err := New(Config{APIKey: testKey, BaseURL: baseURL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.ReverseGeocode(context.Background(), 60.17, 24.94)
	checkErr(t, err, place.ErrTransient)
}

// kyotoSearchBody is a forward-geocoding reply (format=json) with a city, a
// country, a venue that must be dropped and a duplicate city.
const kyotoSearchBody = `{"results":[
	{"result_type":"city","place_id":"p1","city":"Kyoto","country":"Japan","lat":35.0116,"lon":135.7681},
	{"result_type":"amenity","place_id":"p2","name":"Kyoto Station","city":"Kyoto","country":"Japan","lat":34.98,"lon":135.75},
	{"result_type":"country","place_id":"p3","country":"Japan","lat":36.57,"lon":139.24},
	{"result_type":"city","place_id":"p1","city":"Kyoto","country":"Japan","lat":35.0116,"lon":135.7681},
	{"result_type":"city","place_id":"p4","name":"Kyōtanabe","country":"Japan","lat":34.81,"lon":135.77}]}`

func TestSearchKeepsDetailedNamedPlaces(t *testing.T) {
	var gotQuery url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/geocode/search" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(kyotoSearchBody))
	}, 0)

	got, err := c.Search(context.Background(), "Kyoto", 8)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotQuery.Get("text") != "Kyoto" || gotQuery.Get("apiKey") != testKey || gotQuery.Get("format") != "json" {
		t.Fatalf("query = %v", gotQuery)
	}
	if len(got) != 4 {
		t.Fatalf("candidates = %+v", got)
	}
	if got[0].Kind != place.KindCity || got[0].Label != "Kyoto, Japan" || got[0].Lng != 135.7681 || got[0].ID != "p1" {
		t.Fatalf("city = %+v", got[0])
	}
	if got[1].Kind != place.KindAmenity || got[1].Label != "Kyoto Station, Kyoto, Japan" {
		t.Fatalf("venue = %+v", got[1])
	}
	if got[2].Kind != place.KindCountry || got[2].Name != "Japan" || got[2].City != "" {
		t.Fatalf("country = %+v", got[2])
	}
	// A city reported only by name still counts.
	if got[3].City != "Kyōtanabe" || got[3].Label != "Kyōtanabe, Japan" {
		t.Fatalf("named city = %+v", got[3])
	}
}

func TestSearchLimitEmptyAndErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(kyotoSearchBody)) }, 0)
	if got, err := c.Search(context.Background(), "Kyoto", 1); err != nil || len(got) != 1 {
		t.Fatalf("limit 1: %+v %v", got, err)
	}

	empty := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"results":[]}`)) }, 0)
	if got, err := empty.Search(context.Background(), "Nowhere", 8); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no match must be an empty slice: %+v %v", got, err)
	}

	for status, want := range map[int]error{http.StatusTooManyRequests: place.ErrTransient, http.StatusUnauthorized: place.ErrPermanent} {
		failing := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }, 0)
		_, err := failing.Search(context.Background(), "Kyoto", 8)
		checkErr(t, err, want)
	}
	broken := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{`)) }, 0)
	_, err := broken.Search(context.Background(), "Kyoto", 8)
	checkErr(t, err, place.ErrPermanent)
}

func TestSearchFinnishNeighbourhoodSquareAndVenue(t *testing.T) {
	body := `{"results":[
		{"result_type":"suburb","place_id":"otaniemi","suburb":"Otaniemi","city":"Espoo","country":"Finland","lat":60.184,"lon":24.827},
		{"result_type":"street","place_id":"rautatien","street":"Rautatientori","city":"Helsinki","country":"Finland","lat":60.171,"lon":24.942},
		{"result_type":"amenity","place_id":"taproom","name":"Taproom d20","city":"Helsinki","country":"Finland","lat":60.17,"lon":24.94},
		{"result_type":"building","place_id":"anonymous","city":"Espoo","country":"Finland","lat":60.18,"lon":24.83},
		{"result_type":"street","place_id":"bad","street":"Invalid","lat":91,"lon":24.83},
		{"result_type":"suburb","place_id":"otaniemi","suburb":"Otaniemi","city":"Espoo","country":"Finland","lat":60.184,"lon":24.827}
	]}`
	got, err := parseSearch([]byte(body), 8)
	if err != nil || len(got) != 3 {
		t.Fatalf("search: %+v, %v", got, err)
	}
	for i, want := range []struct{ kind, label string }{
		{place.KindLocality, "Otaniemi, Espoo, Finland"},
		{place.KindStreet, "Rautatientori, Helsinki, Finland"},
		{place.KindAmenity, "Taproom d20, Helsinki, Finland"},
	} {
		if got[i].Kind != want.kind || got[i].Label != want.label {
			t.Errorf("candidate %d: %+v", i, got[i])
		}
	}
}
