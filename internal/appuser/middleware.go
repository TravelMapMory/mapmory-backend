package appuser

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/TravelMapMory/mapmory-backend/internal/auth"
)

// Resolver maps a verified authentication subject to an application user.
//
// A resolver must return a database-backed UUID. It must not create
// identities from unverified client-provided values.
type Resolver interface {
	FindOrCreateByAuthSubject(
		ctx context.Context,
		authSubject string,
	) (uuid.UUID, error)
}

// RequireIdentity resolves an authenticated Firebase user to a MapMory user.
//
// It must run after auth.RequireIdentity, which verifies the Firebase ID
// token and attaches the trusted Firebase UID to the request context.
//
// If resolution fails, the request is rejected without calling next.
func RequireIdentity(
	resolver Resolver,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		firebaseIdentity, ok := auth.IdentityFromContext(r.Context())
		if !ok || firebaseIdentity.UID == "" {
			unauthenticated(w)
			return
		}

		id, err := resolver.FindOrCreateByAuthSubject(
			r.Context(),
			firebaseIdentity.UID,
		)
		if err != nil || id == uuid.Nil {
			userResolutionFailed(w)
			return
		}

		ctx := WithIdentity(r.Context(), Identity{ID: id})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// userResolutionFailed reports a server-side identity resolution failure.
//
// Database errors must not be exposed to clients because they may contain
// connection details or other internal implementation information.
func userResolutionFailed(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    "user_resolution_unavailable",
			"message": "unable to resolve application user",
		},
	})
}

// unauthenticated reports a missing or invalid Firebase ID token.
func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    "unauthorized",
			"message": "authentication required",
		},
	})
}
