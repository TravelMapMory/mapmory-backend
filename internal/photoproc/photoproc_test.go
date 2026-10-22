package photoproc

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TravelMapMory/mapmory-backend/internal/photometa"
)

// fixtureDir holds the synthetic images shared with the photometa tests.
const fixtureDir = "../photometa/testdata"

// readFixture returns the bytes of a shared test image, failing the test if it
// cannot be read.
func readFixture(t testing.TB, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// codeOf returns the Code of the *Error in err's chain, or "" if there is none.
func codeOf(err error) string {
	var perr *Error
	if errors.As(err, &perr) {
		return perr.Code
	}
	return ""
}

// TestExtractMetadataFixtures checks statuses, normalized values and diagnostics for each shared fixture.
func TestExtractMetadataFixtures(t *testing.T) {
	noEXIF := []string{"no EXIF metadata", "GPS position missing", "capture time missing", "UTC offset missing"}

	tests := []struct {
		file       string
		gpsStatus  Status
		gps        *photometa.GPS
		timeStatus Status
		timeLocal  string
		offStatus  Status
		offset     string
		diags      []string
	}{
		{"gps.jpg", Usable, &photometa.GPS{Lat: 60.1868333, Lng: 24.8278}, Usable, "2025-07-14T09:30:00", Usable, "+03:00", nil},
		{"gps.png", Usable, &photometa.GPS{Lat: -22.9516667, Lng: -43.2105}, Missing, "", Missing, "",
			[]string{"capture time missing", "UTC offset missing"}},
		{"no_gps.jpg", Missing, nil, Usable, "2025-07-15T18:05:42", Missing, "",
			[]string{"GPS position missing", "UTC offset missing"}},
		{"no_exif.jpg", Missing, nil, Missing, "", Missing, "", noEXIF},
		{"no_exif.png", Missing, nil, Missing, "", Missing, "", noEXIF},
		{"zero_den.jpg", Malformed, nil, Usable, "2025-07-14T09:30:00", Usable, "+03:00", []string{"GPS position malformed"}},
		{"bad_gps_offset.jpg", Malformed, nil, Usable, "2025-07-14T09:30:00", Usable, "+03:00", []string{"GPS position malformed"}},
		{"gps_out_of_range.jpg", Malformed, nil, Usable, "2025-07-14T09:30:00", Usable, "+03:00", []string{"GPS position malformed"}},
		{"bad_time.jpg", Missing, nil, Malformed, "", Malformed, "",
			[]string{"GPS position missing", "capture time malformed", "UTC offset malformed"}},
		{"orientation_6.jpg", Missing, nil, Missing, "", Missing, "",
			[]string{"GPS position missing", "capture time missing", "UTC offset missing"}},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got, err := ExtractMetadata(readFixture(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}

			if got.GPSStatus != tt.gpsStatus {
				t.Errorf("GPSStatus = %q, want %q", got.GPSStatus, tt.gpsStatus)
			}
			if got.CaptureTimeStatus != tt.timeStatus || got.CaptureTimeLocal != tt.timeLocal {
				t.Errorf("capture time = %q (%s), want %q (%s)", got.CaptureTimeLocal, got.CaptureTimeStatus, tt.timeLocal, tt.timeStatus)
			}
			if got.UTCOffsetStatus != tt.offStatus || got.CaptureUTCOffset != tt.offset {
				t.Errorf("UTC offset = %q (%s), want %q (%s)", got.CaptureUTCOffset, got.UTCOffsetStatus, tt.offset, tt.offStatus)
			}
			if !slices.Equal(got.Diagnostics, tt.diags) {
				t.Errorf("Diagnostics = %q, want %q", got.Diagnostics, tt.diags)
			}

			if (got.GPS == nil) != (tt.gps == nil) {
				t.Fatalf("GPS = %+v, want %+v", got.GPS, tt.gps)
			}
			if tt.gps != nil && (math.Abs(got.GPS.Lat-tt.gps.Lat) > 1e-7 || math.Abs(got.GPS.Lng-tt.gps.Lng) > 1e-7) {
				t.Errorf("GPS = %+v, want %+v", *got.GPS, *tt.gps)
			}
		})
	}
}

// TestExtractMetadataUnreadableEXIF checks a corrupt EXIF header marks every field malformed without failing.
func TestExtractMetadataUnreadableEXIF(t *testing.T) {
	data := readFixture(t, "gps.jpg")
	copy(data[12:14], "XX")

	got, err := ExtractMetadata(data)
	if err != nil {
		t.Fatalf("ExtractMetadata returned %v, want a nil error", err)
	}

	for name, status := range map[string]Status{"GPS": got.GPSStatus, "capture time": got.CaptureTimeStatus, "UTC offset": got.UTCOffsetStatus} {
		if status != Malformed {
			t.Errorf("%s status = %q, want %q", name, status, Malformed)
		}
	}
	if got.GPS != nil {
		t.Errorf("GPS = %+v, want nil", *got.GPS)
	}
	if len(got.Diagnostics) == 0 || !strings.HasPrefix(got.Diagnostics[0], "EXIF metadata unreadable") {
		t.Errorf("Diagnostics = %q, want a first line starting with %q", got.Diagnostics, "EXIF metadata unreadable")
	}
}

// TestExtractMetadataUnsupported checks data that is not a JPEG or PNG is refused with unsupported_type.
func TestExtractMetadataUnsupported(t *testing.T) {
	for name, data := range map[string][]byte{"nil": nil, "not an image": []byte("not an image")} {
		t.Run(name, func(t *testing.T) {
			if _, err := ExtractMetadata(data); codeOf(err) != CodeUnsupportedType {
				t.Errorf("error = %v, want code %q", err, CodeUnsupportedType)
			}
		})
	}
}

// TestExtractMetadataFollowsDisplayJPEG checks ExtractMetadata never refuses a
// file DisplayJPEG accepts. image/jpeg tolerates stray bytes after the SOI
// marker, so the JPEG is displayed and its EXIF is reported as unreadable, not
// as an unsupported type.
func TestExtractMetadataFollowsDisplayJPEG(t *testing.T) {
	data := append([]byte{0xFF, 0xD8, 0x00}, readFixture(t, "gps.jpg")[2:]...)

	if _, err := DisplayJPEG(data); err != nil {
		t.Fatalf("DisplayJPEG returned %v, want the file accepted", err)
	}

	got, err := ExtractMetadata(data)
	if err != nil {
		t.Fatalf("ExtractMetadata returned %v, want a nil error", err)
	}
	if got.GPSStatus != Malformed || got.GPS != nil {
		t.Errorf("GPS = %q, %v; want %q, nil", got.GPSStatus, got.GPS, Malformed)
	}
}

// TestCaptureTime checks the EXIF date normalization and its three outcomes.
func TestCaptureTime(t *testing.T) {
	tests := []struct {
		raw    string
		want   string
		status Status
	}{
		{"2025:07:14 09:30:00", "2025-07-14T09:30:00", Usable},
		{"", "", Missing},
		{"0000:00:00 00:00:00", "", Missing},
		{"    :  :     :  :  ", "", Missing},
		{"2025:13:45 99:00:00", "", Malformed},
		{"2025:02:30 10:00:00", "", Malformed},
		{"2025-07-14 09:30:00", "", Malformed},
		{"2025:07:14", "", Malformed},
		{"2025:07:14 09:30:00 ", "", Malformed},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, status := captureTime(tt.raw)
			if got != tt.want || status != tt.status {
				t.Errorf("captureTime(%q) = %q, %s; want %q, %s", tt.raw, got, status, tt.want, tt.status)
			}
		})
	}
}

// TestUTCOffset checks offset validation, including the minute and hour ranges.
func TestUTCOffset(t *testing.T) {
	tests := []struct {
		raw    string
		status Status
	}{
		{"+03:00", Usable},
		{"-12:00", Usable},
		{"+14:00", Usable},
		{"+05:45", Usable},
		{"+00:00", Usable},
		{"", Missing},
		{"   :  ", Missing},
		{"+0300", Malformed},
		{"03:00", Malformed},
		{"Z", Malformed},
		{"+15:00", Malformed},
		{"+03:60", Malformed},
		{"+25:00", Malformed},
		{"+3h", Malformed},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, status := utcOffset(tt.raw)
			want := ""
			if tt.status == Usable {
				want = tt.raw
			}
			if got != want || status != tt.status {
				t.Errorf("utcOffset(%q) = %q, %s; want %q, %s", tt.raw, got, status, want, tt.status)
			}
		})
	}
}
