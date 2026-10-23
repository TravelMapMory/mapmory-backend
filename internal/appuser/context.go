package appuser

import (
	"context"

	"github.com/google/uuid"
)

// Identity identifies a MapMory user by their internal database ID.
//
// The ID must come from server-side user resolution, never directly
// from a client-supplied request field.
type Identity struct {
	ID uuid.UUID
}

// identityKey is private to this package to avoid context key collisions.
type identityKey struct{}

// WithIdentity attaches a resolved application user to a request context.
//
// Call this only after the Firebase identity has been verified and
// the corresponding user has been resolved from the database.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFromContext retrieves the resolved application user.
//
// The second return value is false when no valid application user
// identity is present. Callers must not fall back to a default user.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok && identity.ID != uuid.Nil
}
