// Package geoapify implements place.Provider on top of the Geoapify Reverse
// Geocoding API, using only the standard library.
//
// Request and response structures are private to this package so nothing
// outside it depends on Geoapify's JSON. Failures are mapped to the place
// package's error values, and the API key is never included in an error.
package geoapify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TravelMapMory/mapmory-backend/internal/place"
)

// Client defaults and the fixed credits attached to every result.
const (
	defaultBaseURL = "https://api.geoapify.com"
	defaultTimeout = 10 * time.Second
	providerName   = "geoapify"
	maxBodyBytes   = 1 << 20

	geoapifyCreditText = "Powered by Geoapify"
	geoapifyCreditURL  = "https://www.geoapify.com/"
	osmCreditText      = "© OpenStreetMap contributors"
	osmCreditURL       = "https://www.openstreetmap.org/copyright"
)

// Config holds what New needs to build a Client.
type Config struct {
	// APIKey is the Geoapify key. It is a server-side secret: it is never
	// logged, put in errors, or sent to clients.
	APIKey string
	// BaseURL is the API origin. It defaults to the public Geoapify host and
	// exists so tests can point the client at httptest.
	BaseURL string
	// Timeout bounds one whole request. It defaults to 10 seconds.
	Timeout time.Duration
}

// Client is a place.Provider backed by Geoapify. It is safe for concurrent use.
type Client struct {
	apiKey   string
	endpoint *url.URL
	http     *http.Client
}

// reverseResponse is the part of Geoapify's reverse-geocoding reply (with
// format=json) that this adapter reads.
type reverseResponse struct {
	Results []result `json:"results"`
}

// result is one Geoapify match. Geoapify documents that any field can be
// missing, so every field is optional.
//
// ponytail: Name uses "name" only, so street addresses stay out of photo
// labels; add an address_line1 fallback if the UI wants it. City uses the
// documented "city" only; add town/village if real responses show them.
type result struct {
	Country    string `json:"country"`
	City       string `json:"city"`
	Name       string `json:"name"`
	Datasource struct {
		Attribution string `json:"attribution"`
		URL         string `json:"url"`
	} `json:"datasource"`
}

// New validates cfg and returns a Client. It does not read the environment:
// the wiring code passes the key in explicitly.
func New(cfg Config) (*Client, error) {
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return nil, errors.New("geoapify: API key is required")
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("geoapify: invalid base URL")
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	return &Client{
		apiKey:   key,
		endpoint: base.JoinPath("v1", "geocode", "reverse"),
		http:     &http.Client{Timeout: timeout},
	}, nil
}

// ReverseGeocode asks Geoapify for the place at lat, lng and maps its first
// match to a place.ResolvedPlace.
//
// Errors wrap place.ErrNoResult, place.ErrTransient or place.ErrPermanent, and
// never contain the request URL, the API key or the response body. A canceled
// or expired ctx is transient and still matches context.Canceled or
// context.DeadlineExceeded with errors.Is.
//
// ponytail: no lang or type parameters; add lang when the UI needs localized
// labels.
func (c *Client) ReverseGeocode(ctx context.Context, lat, lng float64) (place.ResolvedPlace, error) {
	body, err := c.get(ctx, lat, lng)
	if err != nil {
		return place.ResolvedPlace{}, err
	}
	return parseReverse(body)
}

// get performs the HTTP request and returns the body of a 200 response. It
// classifies every failure as transient or permanent and keeps the key out of
// the returned errors.
func (c *Client) get(ctx context.Context, lat, lng float64) ([]byte, error) {
	u := *c.endpoint
	u.RawQuery = url.Values{
		"lat":    {strconv.FormatFloat(lat, 'f', -1, 64)},
		"lon":    {strconv.FormatFloat(lng, 'f', -1, 64)},
		"format": {"json"},
		"apiKey": {c.apiKey},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		// The cause is dropped on purpose: it can contain the URL and the key.
		return nil, fmt.Errorf("geoapify: build request: %w", place.ErrPermanent)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		var uerr *url.Error
		// url.Error.Error() includes the full URL, and with it the apiKey.
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, fmt.Errorf("geoapify: request: %w: %w", place.ErrTransient, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		if code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500 {
			return nil, fmt.Errorf("geoapify: HTTP %d: %w", code, place.ErrTransient)
		}
		return nil, fmt.Errorf("geoapify: HTTP %d: %w", code, place.ErrPermanent)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("geoapify: read response: %w: %w", place.ErrTransient, err)
	}
	return body, nil
}

// parseReverse decodes a reverse-geocoding body and maps its first match to a
// place.ResolvedPlace carrying the credits Geoapify's terms require: a
// "Powered by Geoapify" link and the OpenStreetMap credit on every result,
// plus the data source's own credit when it is a different one (for example
// Who's On First or GeoNames).
func parseReverse(body []byte) (place.ResolvedPlace, error) {
	var resp reverseResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return place.ResolvedPlace{}, fmt.Errorf("geoapify: decode response: %w", place.ErrPermanent)
	}
	if len(resp.Results) == 0 {
		return place.ResolvedPlace{}, fmt.Errorf("geoapify: %w", place.ErrNoResult)
	}

	r := resp.Results[0]
	attrs := []place.Attribution{
		{Text: geoapifyCreditText, URL: geoapifyCreditURL},
		{Text: osmCreditText, URL: osmCreditURL},
	}
	if a := r.Datasource.Attribution; a != "" && a != osmCreditText {
		attrs = append(attrs, place.Attribution{Text: a, URL: r.Datasource.URL})
	}

	return place.ResolvedPlace{
		Country:      r.Country,
		City:         r.City,
		Name:         r.Name,
		Provider:     providerName,
		Attributions: attrs,
	}, nil
}
