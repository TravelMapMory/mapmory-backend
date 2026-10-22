package auth

import (
	"context"
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

	return client, nil
}
