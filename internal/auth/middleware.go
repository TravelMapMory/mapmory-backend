package auth

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
)

// RequireIdentity protects a handler with Firebase
// ID token verification.
func RequireIdentity(
	verifier Verifier,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		header := r.Header.Get("Authorization")

		// Require exactly: Bearer <token>
		parts := strings.Fields(header)
		if len(parts) != 2 ||
			!strings.EqualFold(parts[0], "Bearer") ||
			parts[1] == "" {
			unauthorized(w)
			return
		}

		// The Firebase Admin SDK validates signature,
		// audience, issuer, expiration, and other claims.
		token, err := verifier.VerifyIDToken(
			r.Context(),
			parts[1],
		)
		// A 401 tells the client to refresh or sign in again; an
		// infrastructure failure must not, so it is a 503 or 500 (#38).
		switch {
		case errors.Is(err, ErrVerifierUnavailable):
			log.Printf("token verification unavailable: %v", err)
			writeAuthError(w, http.StatusServiceUnavailable, "auth_unavailable",
				"sign-in verification is temporarily unavailable; try again shortly")
			return
		case errors.Is(err, ErrVerifierFailed):
			log.Printf("token verification failed: %v", err)
			writeAuthError(w, http.StatusInternalServerError, "internal_error", "an unexpected error occurred")
			return
		case err != nil || token == nil || token.UID == "":
			unauthorized(w)
			return
		}

		// Only use identity returned by the verifier.
		ctx := WithIdentity(
			r.Context(),
			Identity{UID: token.UID},
		)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthorized(w http.ResponseWriter) {
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

// writeAuthError writes a non-credential authentication failure. It never
// includes the token or the underlying error.
func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "5")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
