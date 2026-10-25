//go:build integration

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	firebaseauth "firebase.google.com/go/v4/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/postgres"
	"github.com/google/uuid"
)

const tripHTTPIntegrationTimeout = 10 * time.Second

// Open only the isolated database supplied by the integration test harness.
// Never fall back to the application's DATABASE_URL.
func tripHTTPIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MAPMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("MAPMORY_TEST_DATABASE_URL is required; run scripts/test-postgres-integration.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), tripHTTPIntegrationTimeout)
	defer cancel()
	db, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close integration database: %v", err)
		}
	})
	return db
}

// Every test uses unique Firebase subjects. Clean up through the fixture pool,
// which remains open independently of any pool used by a reconstructed server.
func tripHTTPFixtureSubject(t *testing.T, db *sql.DB) string {
	t.Helper()
	subject := "trip-http-test-" + uuid.NewString()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), tripHTTPIntegrationTimeout)
		defer cancel()
		if _, err := db.ExecContext(ctx, `DELETE FROM trips WHERE owner_id IN (SELECT id FROM users WHERE auth_subject = $1)`, subject); err != nil {
			t.Errorf("delete fixture trips for %s: %v", subject, err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE auth_subject = $1`, subject); err != nil {
			t.Errorf("delete fixture user for %s: %v", subject, err)
		}
	})
	return subject
}

// Tokens are synthetic and verification is deterministic. User resolution and
// trip persistence are real production repositories backed by PostgreSQL.
func tripHTTPIntegrationHandler(db *sql.DB, subjects map[string]string) http.Handler {
	verifier := endpointVerifierFunc(func(_ context.Context, token string) (*firebaseauth.Token, error) {
		subject, ok := subjects[token]
		if !ok {
			return nil, errors.New("invalid test token")
		}
		return &firebaseauth.Token{UID: subject}, nil
	})
	deps := newDatabaseDependencies(db)
	return newHandlerWithPersistentTrips(verifier, deps.users, deps.trips)
}

func tripHTTPJSON[T any](t *testing.T, status int, body []byte, expectedStatus int) T {
	t.Helper()
	if status != expectedStatus {
		t.Fatalf("status = %d, want %d; body=%s", status, expectedStatus, body)
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode HTTP response: %v; body=%s", err, body)
	}
	return out
}

func TestPersistentTripHTTPUserIsolationIntegration(t *testing.T) {
	db := tripHTTPIntegrationDB(t)
	aliceSubject := tripHTTPFixtureSubject(t, db)
	bobSubject := tripHTTPFixtureSubject(t, db)
	h := tripHTTPIntegrationHandler(db, map[string]string{
		"alice-token": aliceSubject,
		"bob-token":   bobSubject,
	})

	create := func(token, title string) trip {
		t.Helper()
		rec := tripRequest(h, http.MethodPost, "/api/trips", `{"title":"`+title+`","notes":"integration"}`, "Bearer "+token)
		return tripHTTPJSON[trip](t, rec.Code, rec.Body.Bytes(), http.StatusCreated)
	}
	aliceTrip := create("alice-token", "Alice trip")
	bobTrip := create("bob-token", "Bob trip")
	if aliceTrip.ID == bobTrip.ID {
		t.Fatal("two users received the same trip ID")
	}

	for _, tc := range []struct {
		name, token, ownID, foreignID string
	}{
		{"alice", "alice-token", aliceTrip.ID, bobTrip.ID},
		{"bob", "bob-token", bobTrip.ID, aliceTrip.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := tripRequest(h, http.MethodGet, "/api/trips", "", "Bearer "+tc.token)
			page := tripHTTPJSON[tripPage](t, list.Code, list.Body.Bytes(), http.StatusOK)
			if len(page.Items) != 1 || page.Items[0].ID != tc.ownID || page.NextCursor != nil {
				t.Fatalf("unexpected owner-scoped list: %+v", page)
			}

			own := tripRequest(h, http.MethodGet, "/api/trips/"+tc.ownID, "", "Bearer "+tc.token)
			got := tripHTTPJSON[trip](t, own.Code, own.Body.Bytes(), http.StatusOK)
			if got.ID != tc.ownID {
				t.Fatalf("got trip %s, want %s", got.ID, tc.ownID)
			}

			foreign := tripRequest(h, http.MethodGet, "/api/trips/"+tc.foreignID, "", "Bearer "+tc.token)
			missing := tripRequest(h, http.MethodGet, "/api/trips/"+uuid.NewString(), "", "Bearer "+tc.token)
			if foreign.Code != http.StatusNotFound || missing.Code != http.StatusNotFound || foreign.Body.String() != missing.Body.String() {
				t.Fatalf("foreign and unknown trip responses differ: foreign=%d %s unknown=%d %s", foreign.Code, foreign.Body.String(), missing.Code, missing.Body.String())
			}
		})
	}

	// owner_id is not a permitted input field; the request must be rejected.
	ownerSpoof := tripRequest(h, http.MethodPost, "/api/trips", `{"title":"spoof","owner_id":"`+uuid.NewString()+`"}`, "Bearer alice-token")
	if ownerSpoof.Code != http.StatusBadRequest {
		t.Fatalf("client-supplied owner accepted: %d %s", ownerSpoof.Code, ownerSpoof.Body.String())
	}

	anonymous := tripRequest(h, http.MethodGet, "/api/trips", "", "")
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list status = %d; body=%s", anonymous.Code, anonymous.Body.String())
	}
}

func TestPersistentTripHTTPPersistsAcrossPoolsIntegration(t *testing.T) {
	fixtureDB := tripHTTPIntegrationDB(t)
	subject := tripHTTPFixtureSubject(t, fixtureDB)
	subjects := map[string]string{"test-token": subject}

	openPool := func() *sql.DB {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), tripHTTPIntegrationTimeout)
		defer cancel()
		db, err := postgres.Open(ctx, os.Getenv("MAPMORY_TEST_DATABASE_URL"))
		if err != nil {
			t.Fatalf("open server pool: %v", err)
		}
		return db
	}

	firstPool := openPool()
	firstHandler := tripHTTPIntegrationHandler(firstPool, subjects)
	createdResponse := tripRequest(firstHandler, http.MethodPost, "/api/trips", `{"title":"Persistent trip","notes":"survives pool close"}`, "Bearer test-token")
	created := tripHTTPJSON[trip](t, createdResponse.Code, createdResponse.Body.Bytes(), http.StatusCreated)
	if _, err := uuid.Parse(created.ID); err != nil {
		t.Fatalf("created trip has invalid ID: %q", created.ID)
	}
	if err := firstPool.Close(); err != nil {
		t.Fatalf("close first server pool: %v", err)
	}

	secondPool := openPool()
	defer func() {
		if err := secondPool.Close(); err != nil {
			t.Errorf("close second server pool: %v", err)
		}
	}()
	secondHandler := tripHTTPIntegrationHandler(secondPool, subjects)
	retrievedResponse := tripRequest(secondHandler, http.MethodGet, "/api/trips/"+created.ID, "", "Bearer test-token")
	retrieved := tripHTTPJSON[trip](t, retrievedResponse.Code, retrievedResponse.Body.Bytes(), http.StatusOK)
	if retrieved.ID != created.ID || retrieved.Title != created.Title || retrieved.Notes != created.Notes || !retrieved.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("trip changed across new server/pool: created=%+v retrieved=%+v", created, retrieved)
	}
}
