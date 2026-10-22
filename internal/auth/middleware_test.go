package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	firebaseauth "firebase.google.com/go/v4/auth"
)

type fakeVerifier struct {
	uid string
}

func (f fakeVerifier) VerifyIDToken(
	_ context.Context,
	token string,
) (*firebaseauth.Token, error) {
	if token != "valid-token" {
		return nil, errors.New("invalid token")
	}
	return &firebaseauth.Token{UID: f.uid}, nil
}

func TestRequireIdentityRejectsInvalidCredentials(t *testing.T) {
	tests := map[string]string{
		"missing header": "",
		"wrong scheme":   "Basic abc",
		"empty bearer":   "Bearer",
		"invalid token":  "Bearer invalid",
		"extra token":    "Bearer valid-token extra",
	}

	handler := RequireIdentity(
		fakeVerifier{uid: "user-123"},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("unauthenticated request reached handler")
		}),
	)

	for name, header := range tests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", header)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestRequireIdentityPassesVerifiedUID(t *testing.T) {
	handler := RequireIdentity(
		fakeVerifier{uid: "user-123"},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFromContext(r.Context())
			if !ok || identity.UID != "user-123" {
				t.Errorf("identity = %+v, found = %v", identity, ok)
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer valid-token")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}
