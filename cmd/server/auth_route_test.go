package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	firebaseauth "firebase.google.com/go/v4/auth"
)

// routeVerifier is deliberately independent of Firebase credentials and network access.
type routeVerifier struct{}

func (routeVerifier) VerifyIDToken(_ context.Context, token string) (*firebaseauth.Token, error) {
	if token != "valid-token" {
		return nil, errors.New("invalid token")
	}
	return &firebaseauth.Token{UID: "user-123"}, nil
}

func TestAuthMeRoute(t *testing.T) {
	handler := newHandlerWithVerifier(routeVerifier{})

	tests := []struct {
		name, authorization string
		status              int
		uid                 string
	}{
		{name: "missing token", status: http.StatusUnauthorized},
		{name: "invalid token", authorization: "Bearer invalid", status: http.StatusUnauthorized},
		{name: "verified token", authorization: "Bearer valid-token", status: http.StatusOK, uid: "user-123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.status, rec.Body.String())
			}
			if rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("unexpected Content-Type: %q", rec.Header().Get("Content-Type"))
			}

			if tt.status == http.StatusUnauthorized {
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "unauthorized" {
					t.Fatalf("unexpected 401 response: %s (%v)", rec.Body.String(), err)
				}
				if rec.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Error("401 response should include WWW-Authenticate: Bearer")
				}
				return
			}

			var body struct {
				UID string `json:"uid"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.UID != tt.uid {
				t.Errorf("uid = %q, want %q", body.UID, tt.uid)
			}
		})
	}
}
