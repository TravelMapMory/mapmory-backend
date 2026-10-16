package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureDir = "../../internal/photometa/testdata/"

// wantBlock builds the expected report for a fixture. Size and SHA-256 come
// from the file itself so regenerating the fixtures never breaks the test.
func wantBlock(t *testing.T, path, format, exif, lat, lng, dto, oto string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return fmt.Sprintf("file:               %s\n"+
		"format:             %s\n"+
		"size_bytes:         %d\n"+
		"sha256:             %x\n"+
		"exif:               %s\n"+
		"gps_latitude:       %s\n"+
		"gps_longitude:      %s\n"+
		"DateTimeOriginal:   %s\n"+
		"OffsetTimeOriginal: %s\n\n",
		path, format, len(data), sha256.Sum256(data), exif, lat, lng, dto, oto)
}

// TestRun checks a mixed run: the reports for the readable files come out in
// order on stdout, each failing file adds one stderr line and does not stop the
// others, and the exit status is 1.
func TestRun(t *testing.T) {
	dir := t.TempDir()
	gps := fixtureDir + "gps.jpg"
	noGPS := fixtureDir + "no_gps.jpg"
	absent := filepath.Join(dir, "absent.jpg")
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	got := run([]string{gps, absent, notes, noGPS}, &stdout, &stderr)

	if got != 1 {
		t.Errorf("exit status = %d, want 1", got)
	}

	want := wantBlock(t, gps, "jpeg", "present", "60.1868333", "24.8278000", `"2025:07:14 09:30:00"`, `"+03:00"`) +
		wantBlock(t, noGPS, "jpeg", "present", "missing", "missing", `"2025:07:15 18:05:42"`, "missing")
	if stdout.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}

	lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr has %d lines, want 2:\n%s", len(lines), stderr.String())
	}
	for i, path := range []string{absent, notes} {
		if prefix := "photometa: " + path + ": "; !strings.HasPrefix(lines[i], prefix) {
			t.Errorf("stderr line %d = %q, want prefix %q", i, lines[i], prefix)
		}
	}
}

// TestRunUsage checks that running with no files prints usage and exits 2.
func TestRunUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if got := run(nil, &stdout, &stderr); got != 2 {
		t.Errorf("exit status = %d, want 2", got)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("stderr = %q, want a usage message", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}
