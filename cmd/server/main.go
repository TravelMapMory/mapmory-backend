// Command server runs the MapMory backend HTTP service.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TravelMapMory/mapmory-backend/internal/appuser"
	"github.com/TravelMapMory/mapmory-backend/internal/auth"
	"github.com/TravelMapMory/mapmory-backend/internal/photostorage"
	"github.com/TravelMapMory/mapmory-backend/internal/place"
	"github.com/TravelMapMory/mapmory-backend/internal/place/geoapify"
	"github.com/TravelMapMory/mapmory-backend/internal/postgres"
	"github.com/google/uuid"
)

type server struct {
	trips *memoryTripStore
}

// writeJSON writes a JSON response with the given status code and value.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode JSON response: %v", err)
	}
}

// writeError writes a JSON error response with the given status code, error code, and message.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{
		Error: errorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// healthHandler answers liveness probes with a fixed JSON document so that
// deployment platforms and CI can confirm the process is serving traffic.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status: "ok",
	})
}

// listTripsHandler returns a JSON document with the list of trips and a next_cursor
// field that is always null, since pagination is not implemented yet.
func (s *server) listTripsHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	writeJSON(w, http.StatusOK, tripPage{
		Items:      s.trips.list(),
		NextCursor: nil,
	})
}

// createTripHandler creates a new trip with the given title and notes, returning the
// created trip as JSON.
func (s *server) createTripHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	var input createTripRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&input); err != nil {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_request",
			"invalid request body",
		)
		return
	}

	title := strings.TrimSpace(input.Title)

	if title == "" {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_request",
			"title must not be blank",
		)
		return
	}

	if utf8.RuneCountInString(title) > 120 {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_request",
			"title must be at most 120 characters",
		)
		return
	}

	input.Title = title

	created := trip{
		ID:         uuid.NewString(),
		Title:      input.Title,
		Notes:      input.Notes,
		CreatedAt:  time.Now().UTC(),
		PhotoCount: 0,
		Cover:      nil,
	}

	s.trips.add(created)

	writeJSON(w, http.StatusCreated, created)
}

// getTripHandler returns an existing trip or a contract-shaped not_found error.
func (s *server) getTripHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("trip_id")
	// Invalid identifiers cannot name a trip. Keep the response indistinguishable
	// from an unknown resource, rather than exposing a router-generated text 404.
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}

	found, ok := s.trips.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	writeJSON(w, http.StatusOK, found)
}

// newHandler creates a new HTTP handler with the necessary routes and handlers
// for the MapMory backend service.
func newHandler() http.Handler {
	s := &server{
		trips: newMemoryTripStore(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("GET /api/trips", s.listTripsHandler)
	mux.HandleFunc("POST /api/trips", s.createTripHandler)
	mux.HandleFunc("GET /api/trips/{trip_id}", s.getTripHandler)

	return mux
}

// authMeHandler returns the authenticated user's identity as JSON, or a 401 Unauthorized
// response if the request is not authenticated.
func authMeHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeError(
			w,
			http.StatusUnauthorized,
			"unauthorized",
			"authentication required",
		)
		return
	}

	writeJSON(w, http.StatusOK, struct {
		UID string `json:"uid"`
	}{
		UID: identity.UID,
	})
}

// userMeHandler returns the resolved application-user identity.
//
// Both identifiers are obtained from trusted request context values,
// populated by the authentication and user-resolution middleware.
func userMeHandler(w http.ResponseWriter, r *http.Request) {
	firebaseIdentity, firebaseOK := auth.IdentityFromContext(r.Context())
	userIdentity, userOK := appuser.IdentityFromContext(r.Context())

	if !firebaseOK || !userOK {
		writeError(
			w,
			http.StatusUnauthorized,
			"unauthorized",
			"application identity required",
		)
		return
	}

	writeJSON(w, http.StatusOK, struct {
		ID          string `json:"id"`
		AuthSubject string `json:"auth_subject"`
	}{
		ID:          userIdentity.ID.String(),
		AuthSubject: firebaseIdentity.UID,
	})
}

// newHandlerWithVerifier creates a new HTTP handler with the necessary routes and handlers
// for the MapMory backend service, including authentication middleware that verifies
// Firebase ID tokens using the provided verifier.
func newHandlerWithVerifier(
	verifier auth.Verifier,
) http.Handler {
	mux := http.NewServeMux()

	// Reuse existing trip routes for now.
	mux.Handle("/", newHandler())

	// Demonstrate authenticated identity.
	mux.Handle(
		"GET /api/auth/me",
		auth.RequireIdentity(
			verifier,
			http.HandlerFunc(authMeHandler),
		),
	)

	return mux
}

// newHandlerWithResolver extends the existing API with database-backed
// application-user resolution.
//
// Firebase verification always runs before the user resolver. Existing
// endpoints retain their current behavior.
func newHandlerWithResolver(
	verifier auth.Verifier,
	resolver appuser.Resolver,
) http.Handler {
	mux := http.NewServeMux()

	// Preserve all existing routes, including the Firebase-only
	// /api/auth/me diagnostic endpoint.
	mux.Handle("/", newHandlerWithVerifier(verifier))

	mux.Handle(
		"GET /api/users/me",
		auth.RequireIdentity(
			verifier,
			appuser.RequireIdentity(
				resolver,
				http.HandlerFunc(userMeHandler),
			),
		),
	)

	return mux
}

// newConfiguredHandler constructs the HTTP handler and initializes any
// database dependencies explicitly enabled by server configuration.
//
// The returned cleanup function releases resources owned by the handler.
// The caller must invoke it during server shutdown.
func newConfiguredHandler(
	ctx context.Context,
	verifier auth.Verifier,
) (http.Handler, func(), error) {
	switch value := os.Getenv("MAPMORY_ENABLE_DB_USER_RESOLUTION"); value {
	case "", "false":
		return newHandlerWithVerifier(verifier), func() {}, nil

	case "true":
		// Continue initializing PostgreSQL.

	default:
		return nil, nil, fmt.Errorf(
			"invalid MAPMORY_ENABLE_DB_USER_RESOLUTION value %q: expected true or false",
			value,
		)
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if strings.TrimSpace(databaseURL) == "" {
		return nil, nil, errors.New(
			"DATABASE_URL is required when database-backed user resolution is enabled",
		)
	}

	db, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"initialize database-backed user resolution: %w",
			err,
		)
	}

	dependencies := newDatabaseDependencies(db)

	// Place labels and search are optional: without a key, labels stay pending
	// and place search answers 503, while uploads and browsing keep working.
	var resolver *place.Resolver
	var searcher place.Searcher
	if key := strings.TrimSpace(os.Getenv("GEOAPIFY_API_KEY")); key != "" {
		client, err := geoapify.New(geoapify.Config{APIKey: key})
		if err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("initialize place resolution: %w", err)
		}
		resolver, searcher = place.NewResolver(client), client
	} else {
		log.Printf("GEOAPIFY_API_KEY is not set; place labels stay pending and place search is disabled")
	}

	// Photo storage is optional. Explicit local mode keeps the real database,
	// processing, overview and sharing pipeline without requiring GCS.
	var uploadRoutes *uploadHandlers
	var localStore *photostorage.Local
	var store photostorage.Store
	cleanup := func() { _ = db.Close() }
	switch strings.TrimSpace(os.Getenv("MAPMORY_PHOTO_STORAGE")) {
	case "local":
		if os.Getenv("MAPMORY_PHOTO_BUCKET") != "" {
			_ = db.Close()
			return nil, nil, errors.New("unset MAPMORY_PHOTO_BUCKET when MAPMORY_PHOTO_STORAGE=local")
		}
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		var err error
		localStore, err = photostorage.NewLocal(os.Getenv("MAPMORY_LOCAL_PHOTO_DIR"), "http://127.0.0.1:"+port)
		if err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("initialize local photos: %w", err)
		}
		store = localStore
		log.Printf("development-only local photo storage enabled at %s", os.Getenv("MAPMORY_LOCAL_PHOTO_DIR"))
	case "", "gcs":
		if bucket := strings.TrimSpace(os.Getenv("MAPMORY_PHOTO_BUCKET")); bucket != "" {
			gcs, err := photostorage.NewGCS(ctx, bucket, strings.TrimSpace(os.Getenv("MAPMORY_UPLOAD_SIGNER")))
			if err != nil {
				_ = db.Close()
				return nil, nil, fmt.Errorf("initialize photo uploads: %w", err)
			}
			store = gcs
			cleanup = func() { _ = gcs.Close(); _ = db.Close() }
		} else {
			log.Printf("photo storage is not configured; photo upload routes are disabled")
		}
	default:
		_ = db.Close()
		return nil, nil, fmt.Errorf("invalid MAPMORY_PHOTO_STORAGE value %q", os.Getenv("MAPMORY_PHOTO_STORAGE"))
	}
	if store != nil {
		uploadRoutes = newUploadHandlers(dependencies.trips, dependencies.uploads, dependencies.photos, store)
		uploadRoutes.places = resolver
		uploadRoutes.overview = dependencies.overview
		uploadRoutes.shares = newShareHandlers(dependencies.shares, uploadRoutes, searcher)
	}

	// Persistent trip routes require both verified Firebase identity and
	// resolved application-user identity. The legacy handler is used only
	// when database-backed mode is disabled.
	deletion := &deletionHandlers{repo: dependencies.erasure}
	if store != nil {
		deletion.store = store
	}
	handler := withPlaceSearch(withDeletion(
		newPersistentHandler(verifier, dependencies.users, dependencies.trips, uploadRoutes),
		verifier, dependencies.users, deletion), searcher)
	if localStore != nil {
		mux := http.NewServeMux()
		mux.Handle(photostorage.LocalPath, localStore)
		mux.Handle("/", handler)
		return localDevCORS(mux), cleanup, nil
	}
	return handler, cleanup, nil
}

// withPlaceSearch adds the public, rate-limited GET /api/places/search in
// front of next. A nil searcher still mounts the route, answering 503.
func withPlaceSearch(next http.Handler, searcher place.Searcher) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/places/search", &placeSearchHandler{
		searcher: searcher,
		limiter:  newRateLimiter(placeSearchPerSecond, placeSearchPerSecond),
	})
	mux.Handle("/", next)
	return mux
}

// listenAddr returns the address to bind, honouring the PORT variable that
// hosted environments inject and falling back to 8080 for local runs.
func listenAddr() string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return ":" + port
}

// run initializes the backend and serves HTTP until the server stops.
//
// Resources created during initialization are released before run returns,
// including when the HTTP server fails to start.
func run() error {
	if err := loadLocalEnv(".env"); err != nil {
		return fmt.Errorf("load local configuration: %w", err)
	}

	ctx := context.Background()

	verifier, err := auth.NewFirebaseVerifier(
		ctx,
		os.Getenv("FIREBASE_PROJECT_ID"),
	)
	if err != nil {
		return fmt.Errorf("initialize authentication: %w", err)
	}

	handler, cleanup, err := newConfiguredHandler(ctx, verifier)
	if err != nil {
		return fmt.Errorf("initialize server: %w", err)
	}
	defer cleanup()

	addr := listenAddr()
	if os.Getenv("MAPMORY_ENABLE_DB_USER_RESOLUTION") == "true" && os.Getenv("MAPMORY_PHOTO_STORAGE") == "local" {
		addr = "127.0.0.1" + addr // Never expose local object storage externally.
	}
	log.Printf("listening on %s", addr)

	if err := http.ListenAndServe(addr, handler); err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}

	return nil
}

// main reports startup and server failures after run has released
// any resources it initialized.
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
