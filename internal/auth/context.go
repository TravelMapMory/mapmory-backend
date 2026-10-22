package auth

import "context"

// Identity represents an authenticated Firebase user.
type Identity struct {
	UID string
}

type identityKey struct{}

// WithIdentity attaches verified identity to a context.
func WithIdentity(
	ctx context.Context,
	identity Identity,
) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFromContext retrieves verified identity.
func IdentityFromContext(
	ctx context.Context,
) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok
}
