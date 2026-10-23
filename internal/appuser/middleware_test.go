package appuser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/google/uuid"

	"github.com/TravelMapMory/mapmory-backend/internal/auth"
)

// resolverFunc adapts a function to the Resolver interface, allowing
// each test to specify exactly how user resolution should behave.
type resolverFunc func(context.Context, string) (uuid.UUID, error)

func (f resolverFunc) FindOrCreateByAuthSubject(
	ctx context.Context,
	subject string,
) (uuid.UUID, error) {
	return f(ctx, subject)
}

// verifierFunc adapts a function to the Firebase verifier interface.
type verifierFunc func(context.Context, string) (*firebaseauth.Token, error)

func (f verifierFunc) VerifyIDToken(
	ctx context.Context,
	token string,
) (*firebaseauth.Token, error) {
	return f(ctx, token)
}

func TestRequireIdentityRejectsMissingFirebaseIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		context func(context.Context) context.Context
	}{
		{
			name: "missing identity",
			context: func(ctx context.Context) context.Context {
				return ctx
			},
		},
		{
			name: "empty Firebase UID",
			context: func(ctx context.Context) context.Context {
				return auth.WithIdentity(ctx, auth.Identity{UID: ""})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolverCalled := false
			handlerCalled := false

			resolver := resolverFunc(func(
				context.Context,
				string,
			) (uuid.UUID, error) {
				resolverCalled = true
				return uuid.New(), nil
			})

			handler := RequireIdentity(resolver, http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					handlerCalled = true
					w.WriteHeader(http.StatusNoContent)
				},
			))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(tc.context(req.Context()))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if resolverCalled {
				t.Error("resolver called without a valid Firebase identity")
			}
			if handlerCalled {
				t.Error("application handler called without a valid Firebase identity")
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", got)
			}
			assertErrorCode(t, rec, "unauthorized")
		})
	}
}

func TestRequireIdentityPassesResolvedUser(t *testing.T) {
	const firebaseUID = "firebase-user-123"
	internalID := uuid.New()

	resolverCalls := 0
	handlerCalls := 0

	resolver := resolverFunc(func(
		ctx context.Context,
		subject string,
	) (uuid.UUID, error) {
		resolverCalls++

		if subject != firebaseUID {
			t.Errorf("auth subject = %q, want %q", subject, firebaseUID)
		}
		if err := ctx.Err(); err != nil {
			t.Errorf("unexpected context error: %v", err)
		}
		return internalID, nil
	})

	handler := RequireIdentity(resolver, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			handlerCalls++

			appIdentity, ok := IdentityFromContext(r.Context())
			if !ok {
				t.Error("application identity missing")
			} else if appIdentity.ID != internalID {
				t.Errorf("application ID = %s, want %s", appIdentity.ID, internalID)
			}

			firebaseIdentity, ok := auth.IdentityFromContext(r.Context())
			if !ok || firebaseIdentity.UID != firebaseUID {
				t.Errorf("Firebase identity = %+v, found = %v", firebaseIdentity, ok)
			}

			w.WriteHeader(http.StatusNoContent)
		},
	))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(
		auth.WithIdentity(req.Context(), auth.Identity{UID: firebaseUID}),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if resolverCalls != 1 {
		t.Errorf("resolver calls = %d, want 1", resolverCalls)
	}
	if handlerCalls != 1 {
		t.Errorf("handler calls = %d, want 1", handlerCalls)
	}
}

func TestRequireIdentityRejectsResolutionFailures(t *testing.T) {
	// Use a deliberately sensitive-looking error to verify that internal
	// database diagnostics cannot leak into an HTTP response.
	const databaseDetail = "database password=secret-host=internal"

	for _, tc := range []struct {
		name string
		id   uuid.UUID
		err  error
	}{
		{
			name: "database error",
			id:   uuid.Nil,
			err:  errors.New(databaseDetail),
		},
		{
			name: "resolver returned zero UUID",
			id:   uuid.Nil,
			err:  nil,
		},
		{
			name: "resolver returned ID and error",
			id:   uuid.New(),
			err:  errors.New(databaseDetail),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handlerCalled := false
			resolverCalls := 0

			resolver := resolverFunc(func(
				context.Context,
				string,
			) (uuid.UUID, error) {
				resolverCalls++
				return tc.id, tc.err
			})

			handler := RequireIdentity(resolver, http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					handlerCalled = true
					w.WriteHeader(http.StatusNoContent)
				},
			))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(auth.WithIdentity(
				req.Context(),
				auth.Identity{UID: "verified-user"},
			))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			if resolverCalls != 1 {
				t.Errorf("resolver calls = %d, want 1", resolverCalls)
			}
			if handlerCalled {
				t.Error("application handler called after resolution failure")
			}
			if strings.Contains(rec.Body.String(), databaseDetail) {
				t.Error("response exposed internal database details")
			}

			assertErrorCode(t, rec, "user_resolution_unavailable")
		})
	}
}

func TestRequireIdentityPropagatesRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resolverCalled := false
	handlerCalled := false

	resolver := resolverFunc(func(
		gotCtx context.Context,
		subject string,
	) (uuid.UUID, error) {
		resolverCalled = true

		if subject != "verified-user" {
			t.Errorf("auth subject = %q, want verified-user", subject)
		}
		if !errors.Is(gotCtx.Err(), context.Canceled) {
			t.Errorf("resolver context error = %v, want canceled", gotCtx.Err())
		}

		return uuid.Nil, gotCtx.Err()
	})

	handler := RequireIdentity(resolver, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			handlerCalled = true
		},
	))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithIdentity(
		ctx,
		auth.Identity{UID: "verified-user"},
	))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !resolverCalled {
		t.Error("resolver was not called")
	}
	if handlerCalled {
		t.Error("application handler called after canceled resolution")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestRequireIdentityWithFirebaseVerification(t *testing.T) {
	const validToken = "valid-test-token"
	const firebaseUID = "firebase-user-123"
	internalID := uuid.New()

	for _, tc := range []struct {
		name       string
		token      string
		wantStatus int
		wantCalls  int
	}{
		{
			name:       "missing token",
			token:      "",
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:       "invalid token",
			token:      "invalid-token",
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:       "valid token",
			token:      validToken,
			wantStatus: http.StatusNoContent,
			wantCalls:  1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolverCalls := 0
			handlerCalls := 0

			verifier := verifierFunc(func(
				_ context.Context,
				token string,
			) (*firebaseauth.Token, error) {
				if token != validToken {
					return nil, errors.New("invalid Firebase token")
				}
				return &firebaseauth.Token{UID: firebaseUID}, nil
			})

			resolver := resolverFunc(func(
				_ context.Context,
				subject string,
			) (uuid.UUID, error) {
				resolverCalls++
				if subject != firebaseUID {
					t.Errorf("resolved subject = %q, want %q", subject, firebaseUID)
				}
				return internalID, nil
			})

			applicationHandler := http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					handlerCalls++

					identity, ok := IdentityFromContext(r.Context())
					if !ok || identity.ID != internalID {
						t.Errorf("application identity = %+v, found = %v", identity, ok)
					}
					w.WriteHeader(http.StatusNoContent)
				},
			)

			// Outer middleware executes first: Firebase verification,
			// application-user resolution, then the protected handler.
			handler := auth.RequireIdentity(
				verifier,
				RequireIdentity(resolver, applicationHandler),
			)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if resolverCalls != tc.wantCalls {
				t.Errorf("resolver calls = %d, want %d", resolverCalls, tc.wantCalls)
			}
			if handlerCalls != tc.wantCalls {
				t.Errorf("handler calls = %d, want %d", handlerCalls, tc.wantCalls)
			}
		})
	}
}

// assertErrorCode verifies the existing API error response structure.
func assertErrorCode(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantCode string,
) {
	t.Helper()

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Error.Code != wantCode {
		t.Errorf("error code = %q, want %q", response.Error.Code, wantCode)
	}
}
