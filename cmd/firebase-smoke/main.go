// Command firebase-smoke checks local Firebase Admin SDK setup.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	firebase "firebase.google.com/go/v4"
)

func main() {
	projectID := os.Getenv("FIREBASE_PROJECT_ID")
	if projectID == "" {
		log.Fatal("FIREBASE_PROJECT_ID is not set")
	}

	ctx := context.Background()

	app, err := firebase.NewApp(ctx, &firebase.Config{
		ProjectID: projectID,
	})
	if err != nil {
		log.Fatalf("initialize Firebase app: %v", err)
	}

	authClient, err := app.Auth(ctx)
	if err != nil {
		log.Fatalf("initialize Firebase Auth client: %v", err)
	}

	fmt.Printf("Firebase Auth client initialized for %s\n", projectID)

	_ = authClient
}
