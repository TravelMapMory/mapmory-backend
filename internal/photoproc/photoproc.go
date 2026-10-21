// Package photoproc turns one uploaded JPEG or PNG into what a Photo record
// needs: normalized metadata and an EXIF-free display image.
//
// It works on bytes already in memory and knows nothing about HTTP, Google
// Cloud Storage, the database or geocoding, so it can be called and tested
// without any of them. The two entry points are ExtractMetadata and
// DisplayJPEG; the README describes how the upload worker is expected to use
// them.
package photoproc

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/TravelMapMory/mapmory-backend/internal/photometa"
)

// MaxInputBytes is the largest upload accepted, matching the file size limit in
// the OpenAPI contract.
const MaxInputBytes = 25_000_000

// Error codes that can be persisted as Photo.error_code. The first two reuse
// the contract's UploadRejectionCode values.
const (
	CodeUnsupportedType = "unsupported_type"
	CodeFileTooLarge    = "file_too_large"
	CodeImageTooLarge   = "image_too_large"
	CodeInvalidImage    = "invalid_image"
)

// Error reports that an upload cannot be processed because of the image itself.
// Code is safe to store and show; Err is the underlying cause and is meant for
// logs only.
type Error struct {
	Code string
	Err  error
}

// Error returns the code followed by the cause.
func (e *Error) Error() string { return e.Code + ": " + e.Err.Error() }

// Unwrap returns the underlying cause so errors.Is and errors.As see through
// an Error.
func (e *Error) Unwrap() error { return e.Err }

// Status says whether one optional metadata field was absent, present but
// unusable, or usable.
type Status string

// The three states a metadata field can be in.
const (
	Missing   Status = "missing"
	Malformed Status = "malformed"
	Usable    Status = "usable"
)

// Metadata is the normalized result of ExtractMetadata. A value field is only
// set when its Status is Usable, so callers can map the others to null.
type Metadata struct {
	// GPSStatus describes the EXIF position.
	GPSStatus Status
	// GPS is non-nil only when GPSStatus is Usable; both coordinates are within
	// range and set together.
	GPS *photometa.GPS

	// CaptureTimeStatus describes DateTimeOriginal.
	CaptureTimeStatus Status
	// CaptureTimeLocal is the camera's wall-clock time as "2006-01-02T15:04:05",
	// with no zone: it is never treated as UTC.
	CaptureTimeLocal string

	// UTCOffsetStatus describes OffsetTimeOriginal.
	UTCOffsetStatus Status
	// CaptureUTCOffset is the offset of CaptureTimeLocal from UTC as "+hh:mm".
	// It is only what the file states; it is never guessed.
	CaptureUTCOffset string

	// Diagnostics are human-readable notes for logs, one per missing or
	// malformed field.
	Diagnostics []string
}

// offsetPattern mirrors the capture_utc_offset pattern in the OpenAPI contract.
var offsetPattern = regexp.MustCompile(`^[+-]([0-9]{2}):([0-9]{2})$`)

// ExtractMetadata reads GPS, capture time and UTC offset from a JPEG or PNG and
// classifies each as missing, malformed or usable. Time and offset are judged
// independently and nothing is converted to UTC.
//
// It returns an *Error with CodeUnsupportedType only when data is neither JPEG
// nor PNG. Unreadable EXIF is not an error: every field becomes Malformed and
// Diagnostics says why. It does not decode pixels or check sizes; DisplayJPEG is
// the validator for the image itself.
func ExtractMetadata(data []byte) (Metadata, error) {
	raw, err := photometa.Parse(data)
	if errors.Is(err, photometa.ErrUnsupportedFormat) {
		return Metadata{}, &Error{Code: CodeUnsupportedType, Err: err}
	}
	if err != nil {
		return Metadata{
			GPSStatus:         Malformed,
			CaptureTimeStatus: Malformed,
			UTCOffsetStatus:   Malformed,
			Diagnostics:       []string{"EXIF metadata unreadable: " + err.Error()},
		}, nil
	}

	m := Metadata{GPSStatus: Missing}
	switch {
	case raw.GPS != nil:
		m.GPSStatus, m.GPS = Usable, raw.GPS
	case raw.GPSMalformed:
		m.GPSStatus = Malformed
	}
	m.CaptureTimeLocal, m.CaptureTimeStatus = captureTime(raw.DateTimeOriginal)
	m.CaptureUTCOffset, m.UTCOffsetStatus = utcOffset(raw.OffsetTimeOriginal)

	if !raw.HasEXIF {
		m.Diagnostics = append(m.Diagnostics, "no EXIF metadata")
	}
	for _, f := range []struct {
		name   string
		status Status
	}{
		{"GPS position", m.GPSStatus},
		{"capture time", m.CaptureTimeStatus},
		{"UTC offset", m.UTCOffsetStatus},
	} {
		if f.status != Usable {
			m.Diagnostics = append(m.Diagnostics, f.name+" "+string(f.status))
		}
	}
	return m, nil
}

// isUnknown reports whether an EXIF date or offset string is blank or zeroed.
// Cameras write an unknown value as spaces or zeros with the separators kept;
// "+00:00" is not unknown because its sign survives.
func isUnknown(raw string) bool {
	return strings.Trim(raw, " :0") == ""
}

// captureTime normalizes an EXIF DateTimeOriginal ("2006:01:02 15:04:05") to
// "2006-01-02T15:04:05". Blank or zeroed input is Missing; input that is not a
// real calendar time is Malformed.
func captureTime(raw string) (string, Status) {
	if isUnknown(raw) {
		return "", Missing
	}
	t, err := time.Parse("2006:01:02 15:04:05", raw)
	if err != nil {
		return "", Malformed
	}
	return t.Format("2006-01-02T15:04:05"), Usable
}

// utcOffset validates an EXIF OffsetTimeOriginal as "+hh:mm" or "-hh:mm" with
// hours up to 14 and minutes up to 59. Blank or zeroed input is Missing. It
// does not use time.Parse, which accepts minutes above 59.
func utcOffset(raw string) (string, Status) {
	if isUnknown(raw) {
		return "", Missing
	}
	m := offsetPattern.FindStringSubmatch(raw)
	// Fixed-width digit strings compare numerically.
	if m == nil || m[1] > "14" || m[2] > "59" {
		return "", Malformed
	}
	return raw, Usable
}
