package photometa

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// readFixture loads a committed fixture from testdata.
func readFixture(t testing.TB, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestParseFixtures checks every committed fixture: format detection, EXIF
// presence, the verbatim date strings, and GPS values to within 1e-7 degrees.
// zero_den.jpg and bad_gps_offset.jpg must lose only the GPS position, not the
// dates and not with an error.
func TestParseFixtures(t *testing.T) {
	tests := []struct {
		file         string
		format       string
		hasEXIF      bool
		gps          *GPS
		dto          string
		oto          string
		gpsMalformed bool
		orientation  int
	}{
		{"gps.jpg", "jpeg", true, &GPS{60.1868333, 24.8278}, "2025:07:14 09:30:00", "+03:00", false, 0},
		{"gps.png", "png", true, &GPS{-22.9516667, -43.2105}, "", "", false, 0},
		{"no_gps.jpg", "jpeg", true, nil, "2025:07:15 18:05:42", "", false, 0},
		{"no_exif.jpg", "jpeg", false, nil, "", "", false, 0},
		{"no_exif.png", "png", false, nil, "", "", false, 0},
		{"zero_den.jpg", "jpeg", true, nil, "2025:07:14 09:30:00", "+03:00", true, 0},
		{"bad_gps_offset.jpg", "jpeg", true, nil, "2025:07:14 09:30:00", "+03:00", true, 0},
		{"gps_out_of_range.jpg", "jpeg", true, nil, "2025:07:14 09:30:00", "+03:00", true, 0},
		{"bad_time.jpg", "jpeg", true, nil, "2025:13:45 99:00:00", "+25:00", false, 0},
		{"orientation_3.jpg", "jpeg", true, nil, "", "", false, 3},
		{"orientation_6.jpg", "jpeg", true, nil, "", "", false, 6},
		{"orientation_8.jpg", "jpeg", true, nil, "", "", false, 8},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got, err := Parse(readFixture(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}

			if got.Format != tt.format {
				t.Errorf("Format = %q, want %q", got.Format, tt.format)
			}
			if got.HasEXIF != tt.hasEXIF {
				t.Errorf("HasEXIF = %v, want %v", got.HasEXIF, tt.hasEXIF)
			}
			if got.DateTimeOriginal != tt.dto {
				t.Errorf("DateTimeOriginal = %q, want %q", got.DateTimeOriginal, tt.dto)
			}
			if got.OffsetTimeOriginal != tt.oto {
				t.Errorf("OffsetTimeOriginal = %q, want %q", got.OffsetTimeOriginal, tt.oto)
			}
			if got.GPSMalformed != tt.gpsMalformed {
				t.Errorf("GPSMalformed = %v, want %v", got.GPSMalformed, tt.gpsMalformed)
			}
			if got.Orientation != tt.orientation {
				t.Errorf("Orientation = %d, want %d", got.Orientation, tt.orientation)
			}

			if (got.GPS == nil) != (tt.gps == nil) {
				t.Fatalf("GPS = %+v, want %+v", got.GPS, tt.gps)
			}
			if tt.gps == nil {
				return
			}
			if math.Abs(got.GPS.Lat-tt.gps.Lat) > 1e-7 || math.Abs(got.GPS.Lng-tt.gps.Lng) > 1e-7 {
				t.Errorf("GPS = %+v, want %+v", *got.GPS, *tt.gps)
			}
		})
	}
}

// TestParseErrors checks the inputs that must be rejected rather than reported
// as "no EXIF": unknown signatures, truncated containers, and an EXIF block
// whose TIFF header is wrong.
func TestParseErrors(t *testing.T) {
	badByteOrder := readFixture(t, "gps.jpg")
	// The APP1 segment follows SOI directly, so the TIFF block starts at 2+2+2+6.
	copy(badByteOrder[12:14], "XX")

	tests := []struct {
		name string
		data []byte
	}{
		{"nil", nil},
		{"not an image", []byte("not an image")},
		{"jpeg APP1 overruns the data", readFixture(t, "gps.jpg")[:40]},
		{"bad byte order mark", badByteOrder},
		{"png eXIf header cut short", readFixture(t, "gps.png")[:40]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.data); err == nil {
				t.Error("Parse returned no error")
			}
		})
	}

	if _, err := Parse([]byte("not an image")); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("Parse(not an image) error = %v, want ErrUnsupportedFormat", err)
	}
}

// TestParseJPEGSignature checks a file starting FF D8 is read as a JPEG, the
// two bytes image/jpeg registers, even when the next byte is not a marker. Its
// container is then reported as damaged, not as an unsupported format.
func TestParseJPEGSignature(t *testing.T) {
	data := append([]byte{0xFF, 0xD8, 0x00}, readFixture(t, "gps.jpg")[2:]...)

	_, err := Parse(data)
	if err == nil || errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("Parse error = %v, want a damaged-container error that is not ErrUnsupportedFormat", err)
	}
}

// FuzzParse feeds arbitrary bytes to Parse. It must never panic, and any GPS
// position it reports must be a valid latitude and longitude. Plain go test
// runs only the seed corpus, so it stays deterministic.
func FuzzParse(f *testing.F) {
	names := []string{
		"gps.jpg", "gps.png", "no_gps.jpg", "no_exif.jpg", "no_exif.png", "zero_den.jpg", "bad_gps_offset.jpg",
		"gps_out_of_range.jpg", "bad_time.jpg", "orientation_3.jpg", "orientation_6.jpg", "orientation_8.jpg",
	}
	for _, name := range names {
		f.Add(readFixture(f, name))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Parse(data)
		if err != nil || m.GPS == nil {
			return
		}
		if m.GPSMalformed {
			t.Errorf("GPS set while GPSMalformed is true: %+v", *m.GPS)
		}
		if math.Abs(m.GPS.Lat) > 90 || math.Abs(m.GPS.Lng) > 180 {
			t.Errorf("GPS out of range: %+v", *m.GPS)
		}
	})
}
