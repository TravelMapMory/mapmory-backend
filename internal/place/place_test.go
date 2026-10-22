package place

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
)

// fakeProvider is a Provider test double that returns a canned result and
// records how it was called.
type fakeProvider struct {
	place ResolvedPlace
	err   error
	calls int
	ctx   context.Context
}

// ReverseGeocode records the call and returns the canned place and error.
func (f *fakeProvider) ReverseGeocode(ctx context.Context, lat, lng float64) (ResolvedPlace, error) {
	f.calls++
	f.ctx = ctx
	return f.place, f.err
}

// ctxKey is the type of the context key used to prove context propagation.
type ctxKey struct{}

// TestResolveRejectsInvalidCoordinates checks that out-of-range, NaN and
// infinite coordinates fail with ErrInvalidCoordinates and never reach the
// provider.
func TestResolveRejectsInvalidCoordinates(t *testing.T) {
	tests := []struct {
		name     string
		lat, lng float64
	}{
		{"lat above range", 90.0001, 0},
		{"lat below range", -90.0001, 0},
		{"lng above range", 0, 180.0001},
		{"lng below range", 0, -180.0001},
		{"lat NaN", math.NaN(), 0},
		{"lng NaN", 0, math.NaN()},
		{"lat +Inf", math.Inf(1), 0},
		{"lng -Inf", 0, math.Inf(-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeProvider{place: ResolvedPlace{Country: "Finland"}}
			_, err := NewResolver(fake).Resolve(context.Background(), tt.lat, tt.lng)
			if !errors.Is(err, ErrInvalidCoordinates) {
				t.Fatalf("err = %v, want ErrInvalidCoordinates", err)
			}
			if fake.calls != 0 {
				t.Fatalf("provider called %d times, want 0", fake.calls)
			}
		})
	}
}

// TestResolveAcceptsBoundaryCoordinates checks that the exact limits of the
// valid range are passed on to the provider.
func TestResolveAcceptsBoundaryCoordinates(t *testing.T) {
	tests := []struct {
		name     string
		lat, lng float64
	}{
		{"north-east corner", 90, 180},
		{"south-west corner", -90, -180},
		{"origin", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeProvider{place: ResolvedPlace{Country: "Finland"}}
			if _, err := NewResolver(fake).Resolve(context.Background(), tt.lat, tt.lng); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if fake.calls != 1 {
				t.Fatalf("provider called %d times, want 1", fake.calls)
			}
		})
	}
}

// TestResolveReturnsProviderPlace checks that labels are trimmed, provider
// identity and attributions pass through untouched, and the caller's context
// reaches the provider.
func TestResolveReturnsProviderPlace(t *testing.T) {
	attrs := []Attribution{{Text: "© test", URL: "https://example.test"}}
	fake := &fakeProvider{place: ResolvedPlace{
		Country:      " Finland ",
		City:         " Helsinki ",
		Name:         " Ateneum ",
		Provider:     "fake",
		Attributions: attrs,
	}}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")

	got, err := NewResolver(fake).Resolve(ctx, 60.17, 24.94)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := ResolvedPlace{
		Country:      "Finland",
		City:         "Helsinki",
		Name:         "Ateneum",
		Provider:     "fake",
		Attributions: attrs,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if fake.ctx == nil || fake.ctx.Value(ctxKey{}) != "marker" {
		t.Fatal("provider did not receive the caller's context")
	}
}

// TestResolvePartialLabels checks that a result with only some labels is
// returned without error and keeps the missing levels empty.
func TestResolvePartialLabels(t *testing.T) {
	fake := &fakeProvider{place: ResolvedPlace{Country: "Finland", Provider: "fake"}}

	got, err := NewResolver(fake).Resolve(context.Background(), 60.17, 24.94)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Country != "Finland" || got.City != "" || got.Name != "" {
		t.Fatalf("got %+v, want Country only", got)
	}
}

// TestResolveBlankLabelsIsNoResult checks that labels that are empty or only
// whitespace count as no result.
func TestResolveBlankLabelsIsNoResult(t *testing.T) {
	fake := &fakeProvider{place: ResolvedPlace{Country: " ", City: "\t", Name: "", Provider: "fake"}}

	_, err := NewResolver(fake).Resolve(context.Background(), 60.17, 24.94)
	if !errors.Is(err, ErrNoResult) {
		t.Fatalf("err = %v, want ErrNoResult", err)
	}
}

// TestResolvePassesProviderErrors checks that each provider failure class is
// still recognizable with errors.Is after passing through the resolver.
func TestResolvePassesProviderErrors(t *testing.T) {
	tests := []struct {
		name string
		want error
	}{
		{"no result", ErrNoResult},
		{"transient", ErrTransient},
		{"permanent", ErrPermanent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeProvider{err: fmt.Errorf("fake: %w", tt.want)}
			_, err := NewResolver(fake).Resolve(context.Background(), 60.17, 24.94)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
