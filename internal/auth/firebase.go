package auth

import (
	"context"
	"errors"
	"fmt"

	firebase "firebase.google.com/go/v4"
	firebaseauth "firebase.google.com/go/v4/auth"
)

// Verifier validates a Firebase ID token.
//
// Using an interface allows middleware tests to supply
// a fake verifier without contacting Firebase.
type Verifier interface {
	VerifyIDToken(
		ctx context.Context,
		idToken string,
	) (*firebaseauth.Token, error)
}

// Verification failures that are not the caller's fault (backend #38). A
// Verifier returns them wrapped; any other error means the credential itself
// was rejected.
var (
	// ErrVerifierUnavailable is a temporary infrastructure failure, such as
	// Google's signing keys being unreachable. Clients must not sign out on it.
	ErrVerifierUnavailable = errors.New("token verification temporarily unavailable")
	// ErrVerifierFailed is an unexpected internal failure, such as missing
	// project configuration.
	ErrVerifierFailed = errors.New("token verification failed internally")
)

// firebaseVerifier classifies Firebase Admin SDK errors. Only documented
// credential errors stay credential errors; an unknown error is never treated
// as transient.
type firebaseVerifier struct {
	client *firebaseauth.Client
}

func (v firebaseVerifier) VerifyIDToken(ctx context.Context, idToken string) (*firebaseauth.Token, error) {
	token, err := v.client.VerifyIDToken(ctx, idToken)
	if err == nil {
		return token, nil
	}
	return nil, classifyVerifyError(err)
}

// classifyVerifyError maps an SDK error to a credential error (returned
// as is), ErrVerifierUnavailable or ErrVerifierFailed.
func classifyVerifyError(err error) error {
	switch {
	case firebaseauth.IsIDTokenInvalid(err) || firebaseauth.IsTenantIDMismatch(err):
		return err
	case firebaseauth.IsCertificateFetchFailed(err),
		errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %w", ErrVerifierUnavailable, err)
	default:
		return fmt.Errorf("%w: %w", ErrVerifierFailed, err)
	}
}

// NewFirebaseVerifier initializes the Firebase Admin SDK
// using Application Default Credentials.
func NewFirebaseVerifier(
	ctx context.Context,
	projectID string,
) (Verifier, error) {
	if projectID == "" {
		return nil, fmt.Errorf("Firebase project ID is required")
	}

	app, err := firebase.NewApp(ctx, &firebase.Config{
		ProjectID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Firebase: %w", err)
	}

	client, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize Firebase Auth: %w", err)
	}

	return firebaseVerifier{client: client}, nil
}
