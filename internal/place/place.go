// Package place resolves photo coordinates into normalized place labels
// (country, city and place name) without tying callers to one geocoding
// vendor.
//
// A Resolver validates coordinates and normalizes what a Provider returns;
// adapters for concrete vendors live in subpackages such as place/geoapify.
// Nothing here needs a database, an HTTP server or the network, so callers and
// tests can substitute their own Provider.
package place

import (
	"context"
	"errors"
	"strings"
)

// Attribution is a credit that must be shown next to place labels and kept
// with them when they are stored.
type Attribution struct {
	// Text is the credit as it should be displayed.
	Text string
	// URL is where the credit should link; empty means no link.
	URL string
}

// ResolvedPlace is the provider-independent result of reverse geocoding one
// coordinate pair. Callers never see a vendor's response format.
type ResolvedPlace struct {
	// Country is the country name; empty means the provider had no country.
	Country string
	// City is the city name; empty means the provider had no city.
	City string
	// Name is the most specific named place, such as a museum or a shop; empty
	// for plain addresses and when the provider had none.
	Name string
	// Provider is a stable identifier of the source, such as "geoapify".
	Provider string
	// Attributions are the credits the provider's terms require to be shown
	// with these labels and kept with them when stored.
	Attributions []Attribution
}

// Provider is the one capability a geocoding vendor must offer. Replacing
// Geoapify, or faking it in tests, means implementing only this interface.
//
// Implementations must wrap every failure in ErrNoResult, ErrTransient or
// ErrPermanent, must set ResolvedPlace.Provider and Attributions, and must not
// put API keys or coordinates in returned errors.
type Provider interface {
	// ReverseGeocode returns the place at the given WGS84 coordinates.
	ReverseGeocode(ctx context.Context, lat, lng float64) (ResolvedPlace, error)
}

var (
	// ErrInvalidCoordinates means the input was not a real position on Earth;
	// the caller should reject the request (a 400 at the API boundary).
	ErrInvalidCoordinates = errors.New("place: invalid coordinates")
	// ErrNoResult means the provider answered but had no usable label; the
	// caller should keep the coordinates and leave the labels empty.
	ErrNoResult = errors.New("place: no result")
	// ErrTransient means the lookup may succeed later (timeout, network
	// failure, rate limit, provider outage, or a canceled context); the caller
	// may retry.
	ErrTransient = errors.New("place: transient provider failure")
	// ErrPermanent means retrying the same request will not help (rejected
	// credentials, a malformed request, or a response that cannot be read).
	ErrPermanent = errors.New("place: permanent provider failure")
)

// Resolver turns coordinates into a ResolvedPlace using the Provider chosen at
// wiring time.
type Resolver struct {
	provider Provider
}

// NewResolver returns a Resolver that asks p for every lookup.
func NewResolver(p Provider) *Resolver {
	return &Resolver{provider: p}
}

// Resolve validates the coordinates, asks the provider and returns normalized
// labels.
//
// Invalid coordinates fail with ErrInvalidCoordinates before the provider is
// called, and without echoing the values: photo locations are personal data
// and errors get logged. Provider errors are returned unchanged so callers
// can test them with errors.Is. A result whose labels are all blank is
// ErrNoResult, while a result with only some labels is returned as is, with
// the missing levels left empty.
//
// A photo without coordinates is the caller's concern: it skips Resolve and
// the photo stays awaiting a location.
func (r *Resolver) Resolve(ctx context.Context, lat, lng float64) (ResolvedPlace, error) {
	if !validCoordinates(lat, lng) {
		return ResolvedPlace{}, ErrInvalidCoordinates
	}

	p, err := r.provider.ReverseGeocode(ctx, lat, lng)
	if err != nil {
		return ResolvedPlace{}, err
	}

	p.Country = strings.TrimSpace(p.Country)
	p.City = strings.TrimSpace(p.City)
	p.Name = strings.TrimSpace(p.Name)
	if p.Country == "" && p.City == "" && p.Name == "" {
		return ResolvedPlace{}, ErrNoResult
	}
	return p, nil
}

// validCoordinates reports whether lat and lng are a real WGS84 position:
// latitude in [-90, 90] and longitude in [-180, 180]. NaN fails every
// comparison and the infinities are out of range, so all of them are rejected.
func validCoordinates(lat, lng float64) bool {
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}
