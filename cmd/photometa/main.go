// Command photometa prints the photo metadata MapMory cares about (GPS
// position, DateTimeOriginal, OffsetTimeOriginal) plus the file size and
// SHA-256 for each JPEG or PNG file given on the command line.
//
// Usage:
//
//	go run ./cmd/photometa FILE...
//
// Exit status is 0 when every file was read, 1 when any file failed, and 2
// when no files were given.
package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/TravelMapMory/mapmory-backend/internal/photometa"
)

// missing is printed for every field the file does not carry.
const missing = "missing"

// main runs the command against the process arguments and standard streams.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run inspects each path in order, writing one block per readable file to
// stdout and one error line per failing file to stderr. A failing file does
// not stop the others. It returns the process exit status.
func run(paths []string, stdout, stderr io.Writer) int {
	if len(paths) == 0 {
		fmt.Fprintln(stderr, "usage: photometa FILE...")
		return 2
	}

	status := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "photometa: %s: %v\n", path, err)
			status = 1
			continue
		}

		m, err := photometa.Parse(data)
		if err != nil {
			fmt.Fprintf(stderr, "photometa: %s: %v\n", path, err)
			status = 1
			continue
		}
		writeBlock(stdout, path, data, m)
	}
	return status
}

// writeBlock prints the fixed-format report for one file, followed by an empty
// line. CamelCase labels are verbatim EXIF tags; snake_case labels are derived.
func writeBlock(w io.Writer, path string, data []byte, m photometa.Metadata) {
	exif := missing
	if m.HasEXIF {
		exif = "present"
	}

	lat, lng := missing, missing
	if m.GPS != nil {
		lat = strconv.FormatFloat(m.GPS.Lat, 'f', 7, 64)
		lng = strconv.FormatFloat(m.GPS.Lng, 'f', 7, 64)
	}

	writeField(w, "file", path)
	writeField(w, "format", m.Format)
	writeField(w, "size_bytes", strconv.Itoa(len(data)))
	writeField(w, "sha256", fmt.Sprintf("%x", sha256.Sum256(data)))
	writeField(w, "exif", exif)
	writeField(w, "gps_latitude", lat)
	writeField(w, "gps_longitude", lng)
	writeField(w, "DateTimeOriginal", quoteOrMissing(m.DateTimeOriginal))
	writeField(w, "OffsetTimeOriginal", quoteOrMissing(m.OffsetTimeOriginal))
	fmt.Fprintln(w)
}

// writeField prints one "label: value" line with the values aligned in a
// column. Write errors are ignored: there is nothing useful to do about them.
func writeField(w io.Writer, label, value string) {
	fmt.Fprintf(w, "%-19s %s\n", label+":", value)
}

// quoteOrMissing quotes a stored EXIF string so whitespace and control bytes
// are visible and it can never be mistaken for the unquoted "missing" marker.
func quoteOrMissing(s string) string {
	if s == "" {
		return missing
	}
	return strconv.Quote(s)
}
