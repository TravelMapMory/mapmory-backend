package photostorage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Local is an explicitly selected, loopback-only development Store. Its signed
// URLs are short-lived HMAC bearer capabilities; the filesystem is never served
// directly. Do not use this implementation on an externally reachable server.
type Local struct {
	root    string
	baseURL string
	key     []byte
	now     func() time.Time
}

var _ Store = (*Local)(nil)

type localClaim struct {
	Name   string `json:"name"`
	Method string `json:"method"`
	Type   string `json:"type,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Exp    int64  `json:"exp"`
}

type localMetadata struct {
	ContentType string    `json:"content_type"`
	Created     time.Time `json:"created"`
}

const LocalPath = "/_local/photos"

func NewLocal(root, baseURL string) (*Local, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("local photo base URL must be http://127.0.0.1:<port> or http://localhost:<port>")
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
		return nil, errors.New("local photo URLs must use a loopback host")
	}
	if p, err := strconv.Atoi(u.Port()); err != nil || p < 1 || p > 65535 {
		return nil, errors.New("invalid local photo port")
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("local photo directory is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0700); err != nil {
		return nil, fmt.Errorf("create local photo directory: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &Local{root: absolute, baseURL: strings.TrimRight(baseURL, "/"), key: key, now: time.Now}, nil
}

func validObject(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) != 3 {
		return false
	}
	if parts[0] != "originals" && parts[0] != "display" {
		return false
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return false
	}
	file := parts[2]
	if parts[0] == "display" {
		if !strings.HasSuffix(file, ".jpg") {
			return false
		}
		file = strings.TrimSuffix(file, ".jpg")
	}
	_, err := uuid.Parse(file)
	return err == nil
}

func (s *Local) path(name string) (string, error) {
	if !validObject(name) {
		return "", errors.New("invalid photo object name")
	}
	return filepath.Join(s.root, filepath.FromSlash(name)), nil
}

func (s *Local) sign(c localClaim) (string, error) {
	if _, err := s.path(c.Name); err != nil {
		return "", err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return s.baseURL + LocalPath + "?token=" + url.QueryEscape(token), nil
}

func (s *Local) verify(token string) (localClaim, error) {
	left, right, ok := strings.Cut(token, ".")
	if !ok {
		return localClaim{}, errors.New("invalid token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(left)
	if err != nil {
		return localClaim{}, err
	}
	digest, err := base64.RawURLEncoding.DecodeString(right)
	if err != nil {
		return localClaim{}, err
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	if !hmac.Equal(digest, mac.Sum(nil)) {
		return localClaim{}, errors.New("invalid token signature")
	}
	var c localClaim
	if err := json.Unmarshal(payload, &c); err != nil {
		return localClaim{}, err
	}
	if _, err := s.path(c.Name); err != nil {
		return localClaim{}, err
	}
	if c.Method != http.MethodPut && c.Method != http.MethodGet {
		return localClaim{}, errors.New("invalid token method")
	}
	if s.now().Unix() >= c.Exp {
		return localClaim{}, errors.New("token expired")
	}
	return c, nil
}

func (s *Local) SignUpload(name, contentType string, size int64) (Upload, error) {
	if !strings.HasPrefix(name, "originals/") || size < 1 || size > 25_000_000 || (contentType != "image/jpeg" && contentType != "image/png") {
		return Upload{}, errors.New("invalid local upload")
	}
	expiry := s.now().Add(UploadTTL).UTC().Truncate(time.Second)
	u, err := s.sign(localClaim{Name: name, Method: http.MethodPut, Type: contentType, Size: size, Exp: expiry.Unix()})
	if err != nil {
		return Upload{}, err
	}
	return Upload{Method: http.MethodPut, URL: u, Headers: map[string]string{"Content-Type": contentType}, ExpiresAt: expiry}, nil
}

func (s *Local) SignDisplay(name string) (string, time.Time, error) {
	if !strings.HasPrefix(name, "display/") {
		return "", time.Time{}, errors.New("only display images can be signed for reading")
	}
	expiry := s.now().Add(DisplayTTL).UTC().Truncate(time.Second)
	u, err := s.sign(localClaim{Name: name, Method: http.MethodGet, Exp: expiry.Unix()})
	return u, expiry, err
}

func (s *Local) save(ctx context.Context, name, contentType string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mapmory-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	meta, err := json.Marshal(localMetadata{ContentType: contentType, Created: s.now().UTC()})
	if err != nil {
		return err
	}
	return os.WriteFile(path+".meta", meta, 0600)
}

func (s *Local) Write(ctx context.Context, name, contentType string, data []byte) error {
	return s.save(ctx, name, contentType, data)
}

func (s *Local) Stat(ctx context.Context, name string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	path, err := s.path(name)
	if err != nil {
		return ObjectInfo{}, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ObjectInfo{}, ErrObjectNotFound
	}
	if err != nil {
		return ObjectInfo{}, err
	}
	meta, err := os.ReadFile(path + ".meta")
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("read local photo metadata: %w", err)
	}
	var m localMetadata
	if err := json.Unmarshal(meta, &m); err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Size: info.Size(), ContentType: m.ContentType, Created: m.Created}, nil
}

func (s *Local) Read(ctx context.Context, name string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, ErrObjectTooLarge
	}
	path, err := s.path(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrObjectTooLarge
	}
	return data, nil
}

// ServeHTTP accepts only an HMAC-authorized PUT of an original or GET of a
// display image. CORS is added by the server's loopback-only dev wrapper.
func (s *Local) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claim, err := s.verify(r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, "invalid or expired local photo URL", http.StatusForbidden)
		return
	}
	if r.Method != claim.Method {
		w.Header().Set("Allow", claim.Method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPut {
		if !strings.HasPrefix(claim.Name, "originals/") || r.Header.Get("Content-Type") != claim.Type || r.ContentLength != claim.Size {
			http.Error(w, "invalid upload type or length", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, claim.Size+1))
		if err != nil || int64(len(body)) != claim.Size {
			http.Error(w, "upload size mismatch", http.StatusBadRequest)
			return
		}
		if err := s.save(r.Context(), claim.Name, claim.Type, body); err != nil {
			http.Error(w, "local photo storage failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if !strings.HasPrefix(claim.Name, "display/") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	meta, err := s.Stat(r.Context(), claim.Name)
	if errors.Is(err, ErrObjectNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "local photo storage failed", http.StatusInternalServerError)
		return
	}
	path, _ := s.path(claim.Name)
	w.Header().Set("Content-Type", meta.ContentType)
	http.ServeFile(w, r, path)
}
