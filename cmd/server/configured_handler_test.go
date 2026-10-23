package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewConfiguredHandlerWithoutDatabase(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
	}{
		{
			name: "feature disabled",
			flag: "false",
		},
		{
			name: "feature enabled by empty string",
			flag: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MAPMORY_ENABLE_DB_USER_RESOLUTION", tc.flag)
			t.Setenv("DATABASE_URL", "")

			// No Firebase verifier is needed to test /healthz.
			// The database-backed route must remain unregistered.
			handler, cleanup, err := newConfiguredHandler(
				context.Background(),
				nil,
			)
			if err != nil {
				t.Fatalf("initialize handler: %v", err)
			}
			if handler == nil {
				t.Fatal("expected HTTP handler")
			}
			if cleanup == nil {
				t.Fatal("expected cleanup function")
			}
			defer cleanup()

			// Existing public routes must remain available.
			health := httptest.NewRecorder()
			handler.ServeHTTP(
				health,
				httptest.NewRequest(http.MethodGet, "/healthz", nil),
			)

			if health.Code != http.StatusOK {
				t.Errorf(
					"GET /healthz = %d, want 200",
					health.Code,
				)
			}

			// Without the feature, the application-user endpoint
			// must not be registered.
			userMe := httptest.NewRecorder()
			handler.ServeHTTP(
				userMe,
				httptest.NewRequest(
					http.MethodGet,
					"/api/users/me",
					nil,
				),
			)

			if userMe.Code != http.StatusNotFound {
				t.Errorf(
					"GET /api/users/me = %d, want 404",
					userMe.Code,
				)
			}
		})
	}
}

func TestNewConfiguredHandlerRequiresDatabaseURL(t *testing.T) {
	for _, databaseURL := range []string{"", " \t "} {
		t.Run("url="+databaseURL, func(t *testing.T) {
			t.Setenv("MAPMORY_ENABLE_DB_USER_RESOLUTION", "true")
			t.Setenv("DATABASE_URL", databaseURL)

			handler, cleanup, err := newConfiguredHandler(
				context.Background(),
				nil,
			)

			if err == nil {
				if cleanup != nil {
					cleanup()
				}
				t.Fatal("expected missing DATABASE_URL error")
			}
			if handler != nil {
				t.Error("expected no handler on initialization failure")
			}
			if cleanup != nil {
				t.Error("expected no cleanup function on initialization failure")
			}
			if !strings.Contains(err.Error(), "DATABASE_URL is required") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestNewConfiguredHandlerPropagatesDatabaseFailure(t *testing.T) {
	t.Setenv("MAPMORY_ENABLE_DB_USER_RESOLUTION", "true")
	t.Setenv(
		"DATABASE_URL",
		"postgres://localhost/mapmory?sslmode=disable",
	)

	// A canceled context provides a deterministic initialization failure
	// without needing an unavailable network port or running PostgreSQL.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handler, cleanup, err := newConfiguredHandler(ctx, nil)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if handler != nil {
		t.Error("expected no handler after database failure")
	}
	if cleanup != nil {
		t.Error("expected no cleanup function after database failure")
	}
}

func TestNewConfiguredHandlerRejectsInvalidFeatureFlag(t *testing.T) {
	for _, value := range []string{
		"treu",
		"TRUE",
		"1",
		"yes",
		" ",
	} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			t.Setenv("MAPMORY_ENABLE_DB_USER_RESOLUTION", value)
			t.Setenv("DATABASE_URL", "")

			handler, cleanup, err := newConfiguredHandler(
				context.Background(),
				nil,
			)

			if err == nil {
				if cleanup != nil {
					cleanup()
				}
				t.Fatal("expected invalid feature flag error")
			}

			if handler != nil {
				t.Error("expected no handler")
			}
			if cleanup != nil {
				t.Error("expected no cleanup function")
			}

			if !strings.Contains(
				err.Error(),
				"invalid MAPMORY_ENABLE_DB_USER_RESOLUTION",
			) {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
