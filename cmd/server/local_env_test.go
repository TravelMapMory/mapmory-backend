package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLocalEnv(t *testing.T) {
	for _, key := range []string{"FIREBASE_PROJECT_ID", "DATABASE_URL", "MAPMORY_ENABLE_DB_USER_RESOLUTION", "TEST_PASSWORD", "FIREBASE_ID_TOKEN"} {
		original, exists := os.LookupEnv(key)
		os.Unsetenv(key)
		t.Cleanup(func() {
			if exists {
				os.Setenv(key, original)
			} else {
				os.Unsetenv(key)
			}
		})
	}

	path := filepath.Join(t.TempDir(), ".env")
	data := strings.Join([]string{
		"# Local configuration",
		"FIREBASE_PROJECT_ID=mapmory-development",
		"DATABASE_URL='postgres://localhost/mapmory?sslmode=disable'",
		"MAPMORY_ENABLE_DB_USER_RESOLUTION=true",
		"TEST_PASSWORD=private-test-password",
		"FIREBASE_ID_TOKEN=test-token",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	// A process-level setting must take precedence over .env.
	t.Setenv("FIREBASE_PROJECT_ID", "override-project")
	if err := loadLocalEnv(path); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"FIREBASE_PROJECT_ID":               "override-project",
		"DATABASE_URL":                      "postgres://localhost/mapmory?sslmode=disable",
		"MAPMORY_ENABLE_DB_USER_RESOLUTION": "true",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q; want %q", key, got, want)
		}
	}
	for _, key := range []string{"TEST_PASSWORD", "FIREBASE_ID_TOKEN"} {
		if _, exists := os.LookupEnv(key); exists {
			t.Errorf("%s should not be imported into server environment", key)
		}
	}
}

func TestLoadLocalEnvMissingFile(t *testing.T) {
	if err := loadLocalEnv(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Fatalf("missing optional .env: %v", err)
	}
}

func TestLoadLocalEnvRejectsUnclosedQuote(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("DATABASE_URL='unfinished\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadLocalEnv(path); err == nil {
		t.Fatal("expected malformed configuration error")
	}
}
