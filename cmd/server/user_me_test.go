package main

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
)

// endpointVerifierFunc adapts a function to auth.Verifier.
//
// Each test can control Firebase verification without contacting Firebase.
type endpointVerifierFunc func(context.Context, string) (*firebaseauth.Token, error)

func (f endpointVerifierFunc) VerifyIDToken(
	ctx context.Context,
	token string,
) (*firebaseauth.Token, error) {
	return f(ctx, token)
}

// endpointResolverFunc adapts a function to appuser.Resolver.
//
// Each test can simulate successful provisioning or a database failure.
type endpointResolverFunc func(context.Context, string) (uuid.UUID, error)

func (f endpointResolverFunc) FindOrCreateByAuthSubject(
	ctx context.Context,
	subject string,
) (uuid.UUID, error) {
	return f(ctx, subject)
}

func TestUserMeSuccessfulResolution(t *testing.T) {
	const firebaseUID = "firebase-user-123"
	const bearerToken = "valid-test-token"

	userID := uuid.New()
	verifierCalls := 0
	resolverCalls := 0

	verifier := endpointVerifierFunc(func(
		_ context.Context,
		token string,
	) (*firebaseauth.Token, error) {
		verifierCalls++

		if token != bearerToken {
			t.Errorf("token = %q, want %q", token, bearerToken)
		}

		return &firebaseauth.Token{UID: firebaseUID}, nil
	})

	resolver := endpointResolverFunc(func(
		_ context.Context,
		subject string,
	) (uuid.UUID, error) {
		resolverCalls++

		if subject != firebaseUID {
			t.Errorf("subject = %q, want %q", subject, firebaseUID)
		}

		return userID, nil
	})

	handler := newHandlerWithResolver(verifier, resolver)

	req := httptest.NewRequest(http.MethodGet, "/api/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if verifierCalls != 1 {
		t.Errorf("verifier calls = %d, want 1", verifierCalls)
	}
	if resolverCalls != 1 {
		t.Errorf("resolver calls = %d, want 1", resolverCalls)
	}

	var response struct {
		ID          string `json:"id"`
		AuthSubject string `json:"auth_subject"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if response.ID != userID.String() {
		t.Errorf("id = %q, want %s", response.ID, userID)
	}
	if response.AuthSubject != firebaseUID {
		t.Errorf("auth_subject = %q, want %q", response.AuthSubject, firebaseUID)
	}
}

func TestUserMeRejectsUnauthenticatedRequests(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authorization string
	}{
		{
			name: "missing authorization header",
		},
		{
			name:          "invalid bearer token",
			authorization: "Bearer invalid-token",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolverCalls := 0

			verifier := endpointVerifierFunc(func(
				context.Context,
				string,
			) (*firebaseauth.Token, error) {
				return nil, errors.New("invalid Firebase token")
			})

			resolver := endpointResolverFunc(func(
				context.Context,
				string,
			) (uuid.UUID, error) {
				resolverCalls++
				return uuid.New(), nil
			})

			handler := newHandlerWithResolver(verifier, resolver)

			req := httptest.NewRequest(http.MethodGet, "/api/users/me", nil)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if resolverCalls != 0 {
				t.Errorf("resolver called %d times without authentication", resolverCalls)
			}
		})
	}
}

func TestUserMeRejectsResolutionFailures(t *testing.T) {
	const internalDetail = "postgres password=secret internal-host"

	for _, tc := range []struct {
		name string
		id   uuid.UUID
		err  error
	}{
		{
			name: "database unavailable",
			err:  errors.New(internalDetail),
		},
		{
			name: "resolver returned zero UUID",
		},
		{
			name: "resolver returned ID with error",
			id:   uuid.New(),
			err:  errors.New(internalDetail),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolverCalls := 0

			verifier := endpointVerifierFunc(func(
				context.Context,
				string,
			) (*firebaseauth.Token, error) {
				return &firebaseauth.Token{UID: "verified-user"}, nil
			})

			resolver := endpointResolverFunc(func(
				context.Context,
				string,
			) (uuid.UUID, error) {
				resolverCalls++
				return tc.id, tc.err
			})

			handler := newHandlerWithResolver(verifier, resolver)

			req := httptest.NewRequest(http.MethodGet, "/api/users/me", nil)
			req.Header.Set("Authorization", "Bearer valid-token")

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			if resolverCalls != 1 {
				t.Errorf("resolver calls = %d, want 1", resolverCalls)
			}
			if strings.Contains(rec.Body.String(), internalDetail) {
				t.Error("response exposed internal database details")
			}

			assertEndpointErrorCode(t, rec, "user_resolution_unavailable")
		})
	}
}

func TestUserMeIgnoresClientSuppliedIdentity(t *testing.T) {
	const firebaseUID = "trusted-firebase-user"

	trustedID := uuid.New()
	forgedID := uuid.New()

	verifier := endpointVerifierFunc(func(
		context.Context,
		string,
	) (*firebaseauth.Token, error) {
		return &firebaseauth.Token{UID: firebaseUID}, nil
	})

	resolver := endpointResolverFunc(func(
		_ context.Context,
		subject string,
	) (uuid.UUID, error) {
		if subject != firebaseUID {
			t.Errorf("resolver received untrusted subject %q", subject)
		}
		return trustedID, nil
	})

	handler := newHandlerWithResolver(verifier, resolver)

	// The client attempts to override both the internal UUID and the
	// Firebase subject through query parameters and HTTP headers.
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/users/me?id="+forgedID.String()+"&auth_subject=attacker",
		nil,
	)
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("X-User-ID", forgedID.String())
	req.Header.Set("X-Firebase-UID", "attacker")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var response struct {
		ID          string `json:"id"`
		AuthSubject string `json:"auth_subject"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if response.ID != trustedID.String() {
		t.Errorf("id = %q, want trusted ID %s", response.ID, trustedID)
	}
	if response.AuthSubject != firebaseUID {
		t.Errorf("auth_subject = %q, want %q", response.AuthSubject, firebaseUID)
	}
}

func TestAuthMeRemainsIndependentOfUserResolution(t *testing.T) {
	const firebaseUID = "firebase-only-user"

	resolverCalls := 0

	verifier := endpointVerifierFunc(func(
		context.Context,
		string,
	) (*firebaseauth.Token, error) {
		return &firebaseauth.Token{UID: firebaseUID}, nil
	})

	// Deliberately fail if called. /api/auth/me should work even when
	// the application-user repository is unavailable.
	resolver := endpointResolverFunc(func(
		context.Context,
		string,
	) (uuid.UUID, error) {
		resolverCalls++
		return uuid.Nil, errors.New("database unavailable")
	})

	handler := newHandlerWithResolver(verifier, resolver)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer valid-token")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if resolverCalls != 0 {
		t.Errorf("/api/auth/me called resolver %d times", resolverCalls)
	}

	var response struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.UID != firebaseUID {
		t.Errorf("uid = %q, want %q", response.UID, firebaseUID)
	}
}

func assertEndpointErrorCode(
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
