package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalDevCORS(t *testing.T) {
	h := localDevCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, tt := range []struct {
		origin string
		allow  bool
	}{
		{"http://localhost:5173", true}, {"http://127.0.0.1:5173", true},
		{"http://localhost:4173", true}, {"https://evil.example", false},
		{"http://example.com:5173", false}, {"http://localhost.evil.example:5173", false},
	} {
		r := httptest.NewRequest(http.MethodOptions, "/_local/photos", nil)
		r.Header.Set("Origin", tt.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if tt.allow && (w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != tt.origin) {
			t.Errorf("origin %s: response=%d headers=%v", tt.origin, w.Code, w.Header())
		}
		if !tt.allow && w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("allowed %s", tt.origin)
		}
	}
}
