package geoapify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
