package auth

import (
	"encoding/json"
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
		if err != nil || token == nil || token.UID == "" {
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
