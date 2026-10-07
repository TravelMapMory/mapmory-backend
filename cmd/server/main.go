// Command server runs the MapMory backend HTTP service.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"
)

// healthResponse is the JSON response for the health endpoint.
type healthResponse struct {
	Status string `json:"status"`
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

// listTripsHandler returns an empty list of trips for now, but it is a placeholder
// for the future implementation of the trips listing endpoint.
func listTripsHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, tripPage{
		Items:      []trip{},
		NextCursor: nil,
	})
}

// createTripHandler handles the creation of a new trip. It validates the request body
// and responds with appropriate error messages if the input is invalid.
func createTripHandler(w http.ResponseWriter, r *http.Request) {
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
}

// newHandler returns a handler that serves the health endpoint.
func newHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("GET /api/trips", listTripsHandler)
	mux.HandleFunc("POST /api/trips", createTripHandler)

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
