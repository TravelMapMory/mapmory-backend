// Command server runs the MapMory backend HTTP service.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

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

// listTripsHandler returns a JSON document with an empty list of trips and a nil next_cursor.
func (s *server) listTripsHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	writeJSON(w, http.StatusOK, tripPage{
		Items:      []trip{},
		NextCursor: nil,
	})
}

// createTripHandler handles the creation of a new trip. It validates the request body
// and title, returning appropriate error responses for invalid input.
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

// main wires the routes and serves until the listener fails.
func main() {
	addr := listenAddr()
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, newHandler()); err != nil {
		log.Fatal(err)
	}
}
