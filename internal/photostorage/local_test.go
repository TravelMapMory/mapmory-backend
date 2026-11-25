package photostorage

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLocalUploadProcessAndDisplay(t *testing.T) {
	s, err := NewLocal(t.TempDir(), "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	trip, photo := uuid.New(), uuid.New()
	original := "originals/" + trip.String() + "/" + photo.String()
	display := "display/" + trip.String() + "/" + photo.String() + ".jpg"
	data := []byte("this is test content")
	target, err := s.SignUpload(original, "image/jpeg", int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if target.Method != http.MethodPut || target.Headers["Content-Type"] != "image/jpeg" {
		t.Fatalf("upload target: %+v", target)
	}
	put := httptest.NewRequest(http.MethodPut, target.URL, bytes.NewReader(data))
	put.Header.Set("Content-Type", "image/jpeg")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, put)
	if w.Code != http.StatusOK {
		t.Fatalf("upload status=%d: %s", w.Code, w.Body.String())
	}
	info, err := s.Stat(context.Background(), original)
	if err != nil || info.Size != int64(len(data)) || info.ContentType != "image/jpeg" {
		t.Fatalf("stat=%+v err=%v", info, err)
	}
	got, err := s.Read(context.Background(), original, 100)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read=%q err=%v", got, err)
	}
	if _, err := s.Read(context.Background(), original, 1); !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("limit error=%v", err)
	}
	if err := s.Write(context.Background(), display, "image/jpeg", []byte("display")); err != nil {
		t.Fatal(err)
	}
	signed, _, err := s.SignDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.ServeHTTP(response, httptest.NewRequest(http.MethodGet, signed, nil))
	if response.Code != http.StatusOK || response.Body.String() != "display" {
		t.Fatalf("display status=%d body=%q", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("display must not be cached")
	}
}

func TestLocalTokensCannotReadOriginalsOrBeForged(t *testing.T) {
	s, err := NewLocal(t.TempDir(), "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	original := "originals/" + uuid.NewString() + "/" + uuid.NewString()
	target, err := s.SignUpload(original, "image/png", 4)
	if err != nil {
		t.Fatal(err)
	}
	read := httptest.NewRecorder()
	s.ServeHTTP(read, httptest.NewRequest(http.MethodGet, target.URL, nil))
	if read.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET upload target=%d", read.Code)
	}
	wrongType := httptest.NewRequest(http.MethodPut, target.URL, strings.NewReader("data"))
	wrongType.Header.Set("Content-Type", "image/jpeg")
	check := httptest.NewRecorder()
	s.ServeHTTP(check, wrongType)
	if check.Code != http.StatusBadRequest {
		t.Fatalf("type mismatch=%d", check.Code)
	}
	tooShort := httptest.NewRequest(http.MethodPut, target.URL, strings.NewReader("abc"))
	tooShort.Header.Set("Content-Type", "image/png")
	check = httptest.NewRecorder()
	s.ServeHTTP(check, tooShort)
	if check.Code != http.StatusBadRequest {
		t.Fatalf("size mismatch=%d", check.Code)
	}
	u, _ := url.Parse(target.URL)
	token := u.Query().Get("token")
	if token == "" {
		t.Fatal("missing token")
	}
	u.RawQuery = "token=" + url.QueryEscape(token+"x")
	forged := httptest.NewRecorder()
	s.ServeHTTP(forged, httptest.NewRequest(http.MethodPut, u.String(), strings.NewReader("data")))
	if forged.Code != http.StatusForbidden {
		t.Fatalf("forged token=%d", forged.Code)
	}
	s.now = func() time.Time { return target.ExpiresAt.Add(time.Second) }
	expired := httptest.NewRecorder()
	s.ServeHTTP(expired, httptest.NewRequest(http.MethodPut, target.URL, strings.NewReader("data")))
	if expired.Code != http.StatusForbidden {
		t.Fatalf("expired token=%d", expired.Code)
	}
	if _, _, err := s.SignDisplay(original); err == nil {
		t.Fatal("signed original for GET")
	}
	for _, name := range []string{"../secrets", "display/../../secret", "originals/foo/bar"} {
		if err := s.Write(context.Background(), name, "image/jpeg", []byte("x")); err == nil {
			t.Errorf("accepted invalid path %q", name)
		}
	}
}

func TestLocalStorageValidation(t *testing.T) {
	for _, base := range []string{"https://127.0.0.1:8080", "http://example.com:8080", "http://127.0.0.1", "http://127.0.0.1:0"} {
		if _, err := NewLocal(t.TempDir(), base); err == nil {
			t.Errorf("accepted %s", base)
		}
	}
	s, err := NewLocal(t.TempDir(), "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	name := "originals/" + uuid.NewString() + "/" + uuid.NewString()
	if _, err := s.SignUpload(name, "image/jpeg", 25_000_001); err == nil {
		t.Fatal("oversized upload signed")
	}
	if _, err := s.SignUpload(name, "application/octet-stream", 4); err == nil {
		t.Fatal("unknown MIME signed")
	}
	if _, err := s.Read(context.Background(), name, 5); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("missing=%v", err)
	}
}

func TestLocalDeleteRemovesObjectAndMetadata(t *testing.T) {
	s, err := NewLocal(t.TempDir(), "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	name := "display/00000000-0000-0000-0000-000000000001/00000000-0000-0000-0000-000000000002.jpg"
	if err := s.Write(ctx, name, "image/jpeg", []byte("jpeg")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat(ctx, name); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("after delete: err=%v", err)
	}
	path, _ := s.path(name)
	if _, err := os.Stat(path + ".meta"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata left behind: %v", err)
	}
	// Deleting again, or something never stored, succeeds; a bad name does not.
	if err := s.Delete(ctx, name); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if err := s.Delete(ctx, "../outside"); err == nil {
		t.Fatal("deleted an invalid name")
	}
}
