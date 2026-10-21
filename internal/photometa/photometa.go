// Package photometa reads the few EXIF fields MapMory cares about (GPS
// coordinates, the original capture time and the image orientation) from JPEG
// and PNG files using only the standard library.
//
// It is deliberately not an image validator: once the EXIF block is found and
// read, parsing stops, and callers that need a decodable image must decode it
// themselves. Only JPEG APP1 Exif segments and PNG eXIf chunks are supported;
// HEIC, XMP and legacy PNG text profiles are not.
package photometa

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Metadata is what Parse found in one image file.
type Metadata struct {
	// Format is "jpeg" or "png", detected from the file's magic bytes.
	Format string
	// HasEXIF reports whether the file carries an EXIF block at all.
	HasEXIF bool
	// GPS is nil unless both coordinates are present and valid.
	GPS *GPS
	// GPSMalformed reports that GPS coordinate tags or the GPS IFD pointer are
	// present but unusable: out of range, a zero denominator, a bad hemisphere
	// letter or an unreadable directory. GPS is then nil. It is false when the
	// file simply carries no GPS position.
	GPSMalformed bool
	// DateTimeOriginal is EXIF tag 0x9003 exactly as stored; empty means missing.
	DateTimeOriginal string
	// OffsetTimeOriginal is EXIF tag 0x9011 exactly as stored; empty means missing.
	OffsetTimeOriginal string
	// Orientation is EXIF tag 0x0112 from IFD0 exactly as stored, normally 1-8;
	// 0 means missing or not a single SHORT value.
	Orientation int
}

// GPS is a position in signed WGS84 decimal degrees: north and east are
// positive. It is a pointer in Metadata so latitude and longitude are always
// set or absent together.
type GPS struct {
	// Lat is the latitude in decimal degrees, north positive.
	Lat float64
	// Lng is the longitude in decimal degrees, east positive.
	Lng float64
}

// EXIF and TIFF tags read by this package.
const (
	tagExifIFD            = 0x8769
	tagGPSIFD             = 0x8825
	tagOrientation        = 0x0112
	tagDateTimeOriginal   = 0x9003
	tagOffsetTimeOriginal = 0x9011
	tagGPSLatRef          = 0x0001
	tagGPSLat             = 0x0002
	tagGPSLngRef          = 0x0003
	tagGPSLng             = 0x0004
)

// TIFF value types read by this package.
const (
	typeASCII    = 2
	typeShort    = 3
	typeLong     = 4
	typeRational = 5
	typeIFD      = 13
)

// ErrUnsupportedFormat is returned by Parse for data that is neither JPEG nor
// PNG.
var ErrUnsupportedFormat = errors.New("not a JPEG or PNG file")

var (
	// jpegMagic is the signature image/jpeg registers, so any file the standard
	// decoder takes for a JPEG is a JPEG here too.
	jpegMagic  = []byte{0xFF, 0xD8}
	pngMagic   = []byte("\x89PNG\r\n\x1a\n")
	exifHeader = []byte("Exif\x00\x00")
)

// Parse detects the image format from its magic bytes (never from a file
// name), locates the EXIF block and extracts the fields in Metadata.
//
// It returns an error only when the container or the EXIF header and top-level
// directory are malformed. A damaged GPS or Exif sub-directory, or a bad tag
// value, just leaves that field missing (and sets GPSMalformed for a damaged
// GPS position), so one corrupt value never hides the others. A file without
// EXIF is not an error; HasEXIF is false. Data that is neither JPEG nor PNG
// returns ErrUnsupportedFormat.
func Parse(data []byte) (Metadata, error) {
	var (
		m     Metadata
		block []byte
		found bool
		err   error
	)

	switch {
	case bytes.HasPrefix(data, jpegMagic):
		m.Format = "jpeg"
		block, found, err = jpegEXIF(data)
	case bytes.HasPrefix(data, pngMagic):
		m.Format = "png"
		block, found, err = pngEXIF(data)
	default:
		return Metadata{}, ErrUnsupportedFormat
	}
	if err != nil {
		return Metadata{}, err
	}
	if !found {
		return m, nil
	}

	m.HasEXIF = true
	if err := parseTIFF(block, &m); err != nil {
		return Metadata{}, err
	}
	return m, nil
}

// jpegEXIF walks the JPEG marker segments up to the start of scan and returns
// the TIFF block of the first APP1 segment that starts with "Exif\0\0".
// Standalone RSTn/TEM markers before the scan and multi-segment (extended)
// EXIF are not supported.
func jpegEXIF(data []byte) (block []byte, found bool, err error) {
	i := 2
	for {
		if i >= len(data) || data[i] != 0xFF {
			return nil, false, fmt.Errorf("jpeg: expected a marker at offset %d", i)
		}
		// Any run of 0xFF bytes before the marker code is padding.
		for i+1 < len(data) && data[i+1] == 0xFF {
			i++
		}
		if i+4 > len(data) {
			return nil, false, errors.New("jpeg: data ends before the start of scan")
		}

		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 {
			return nil, false, nil
		}

		length := int(binary.BigEndian.Uint16(data[i+2:]))
		if length < 2 || i+2+length > len(data) {
			return nil, false, fmt.Errorf("jpeg: segment at offset %d has an invalid length %d", i, length)
		}

		payload := data[i+4 : i+2+length]
		if marker == 0xE1 && bytes.HasPrefix(payload, exifHeader) {
			return payload[len(exifHeader):], true, nil
		}
		i += 2 + length
	}
}

// pngEXIF walks the PNG chunks and returns the data of the first eXIf chunk,
// which is a bare TIFF block. It does not stop at IDAT, so an eXIf chunk after
// the pixel data is still found, and it does not verify chunk CRCs.
func pngEXIF(data []byte) (block []byte, found bool, err error) {
	i := 8
	for {
		if len(data)-i < 12 {
			return nil, false, errors.New("png: data ends before the IEND chunk")
		}

		n := uint64(binary.BigEndian.Uint32(data[i:]))
		if n+12 > uint64(len(data)-i) {
			return nil, false, fmt.Errorf("png: chunk at offset %d overruns the data", i)
		}

		switch string(data[i+4 : i+8]) {
		case "eXIf":
			return data[i+8 : i+8+int(n)], true, nil
		case "IEND":
			return nil, false, nil
		}
		i += 12 + int(n)
	}
}

// entry is one 12-byte IFD entry whose value field has not been resolved yet.
type entry struct {
	typ   uint16
	count uint32
	// raw is the 4-byte value field: the value itself if it fits, else an offset.
	raw []byte
}

// reader decodes values out of one TIFF block. All offsets inside the block
// are relative to its first byte, and all size arithmetic is done in uint64 so
// hostile counts and offsets cannot overflow.
type reader struct {
	block []byte
	order binary.ByteOrder
}

// parseTIFF reads the TIFF header and IFD0 of an EXIF block, then fills m from
// the Exif and GPS sub-directories it points to. Only those two fixed pointers
// are followed, so directory loops are impossible.
func parseTIFF(block []byte, m *Metadata) error {
	if len(block) < 8 {
		return errors.New("exif: TIFF header is shorter than 8 bytes")
	}

	var order binary.ByteOrder
	switch string(block[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return errors.New("exif: unknown byte order mark")
	}
	if order.Uint16(block[2:]) != 42 {
		return errors.New("exif: bad TIFF magic number")
	}

	r := reader{block: block, order: order}
	ifd0, ok := r.ifd(uint64(order.Uint32(block[4:])))
	if !ok {
		return errors.New("exif: IFD0 lies outside the EXIF block")
	}

	if exif, ok := r.subIFD(ifd0, tagExifIFD); ok {
		m.DateTimeOriginal = r.ascii(exif[tagDateTimeOriginal])
		m.OffsetTimeOriginal = r.ascii(exif[tagOffsetTimeOriginal])
	}

	m.Orientation = r.short(ifd0[tagOrientation])

	if _, hasGPS := ifd0[tagGPSIFD]; hasGPS {
		// A nil map from an unreadable GPS IFD is safe to index below.
		gps, _ := r.subIFD(ifd0, tagGPSIFD)
		lat, latOK := r.coordinate(gps, tagGPSLatRef, tagGPSLat, 'N', 'S', 90)
		lng, lngOK := r.coordinate(gps, tagGPSLngRef, tagGPSLng, 'E', 'W', 180)
		switch {
		case latOK && lngOK:
			m.GPS = &GPS{Lat: lat, Lng: lng}
		case gps == nil || hasCoordinateTag(gps):
			m.GPSMalformed = true
		}
	}
	return nil
}

// hasCoordinateTag reports whether the GPS directory carries any latitude or
// longitude tag. A directory with only other tags, such as GPSVersionID, counts
// as "no position", not as a damaged one.
func hasCoordinateTag(gps map[uint16]entry) bool {
	for _, tag := range []uint16{tagGPSLatRef, tagGPSLat, tagGPSLngRef, tagGPSLng} {
		if _, ok := gps[tag]; ok {
			return true
		}
	}
	return false
}

// ifd reads the entry table of the directory at off. It reports false when the
// table does not fit inside the block. The next-IFD link is never followed.
func (r reader) ifd(off uint64) (map[uint16]entry, bool) {
	if off+2 > uint64(len(r.block)) {
		return nil, false
	}

	n := uint64(r.order.Uint16(r.block[off:]))
	end := off + 2 + 12*n
	if end > uint64(len(r.block)) {
		return nil, false
	}

	entries := make(map[uint16]entry, n)
	for p := off + 2; p < end; p += 12 {
		entries[r.order.Uint16(r.block[p:])] = entry{
			typ:   r.order.Uint16(r.block[p+2:]),
			count: r.order.Uint32(r.block[p+4:]),
			raw:   r.block[p+8 : p+12],
		}
	}
	return entries, true
}

// subIFD follows a pointer tag (Exif IFD or GPS IFD) in parent and returns the
// directory it points to. A missing, malformed or out-of-range pointer is
// reported as false.
func (r reader) subIFD(parent map[uint16]entry, tag uint16) (map[uint16]entry, bool) {
	e, ok := parent[tag]
	if !ok || (e.typ != typeLong && e.typ != typeIFD) || e.count != 1 {
		return nil, false
	}
	return r.ifd(uint64(r.order.Uint32(e.raw)))
}

// value returns the bytes of e's value given the size in bytes of one element:
// the value field itself when the value fits in 4 bytes, otherwise the bytes at
// the offset stored in it. It reports false if that range is outside the block.
func (r reader) value(e entry, unit uint64) ([]byte, bool) {
	size := unit * uint64(e.count)
	if size <= 4 {
		return e.raw[:size], true
	}

	off := uint64(r.order.Uint32(e.raw))
	if off+size > uint64(len(r.block)) {
		return nil, false
	}
	return r.block[off : off+size], true
}

// ascii returns an ASCII entry's text up to the first NUL, unmodified. It
// returns "" for a missing entry, a wrong type, a bad value offset or an empty
// string, all of which mean "missing".
func (r reader) ascii(e entry) string {
	if e.typ != typeASCII || e.count < 1 {
		return ""
	}

	b, ok := r.value(e, 1)
	if !ok {
		return ""
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// short returns the value of a single-element SHORT entry, or 0 for a missing
// entry, a wrong type or a count other than 1. The 2-byte value sits at the
// start of the 4-byte field in both byte orders.
func (r reader) short(e entry) int {
	if e.typ != typeShort || e.count != 1 {
		return 0
	}
	return int(r.order.Uint16(e.raw))
}

// degrees converts a GPS RATIONAL triple (degrees, minutes, seconds) into
// unsigned decimal degrees. Any zero denominator makes the whole value
// unusable: a position with an undefined component is unsafe for map placement.
func (r reader) degrees(e entry) (float64, bool) {
	if e.typ != typeRational || e.count != 3 {
		return 0, false
	}

	b, ok := r.value(e, 8)
	if !ok {
		return 0, false
	}

	var dms [3]float64
	for i := range dms {
		num, den := r.order.Uint32(b[8*i:]), r.order.Uint32(b[8*i+4:])
		if den == 0 {
			return 0, false
		}
		dms[i] = float64(num) / float64(den)
	}
	return dms[0] + dms[1]/60 + dms[2]/3600, true
}

// coordinate reads one signed GPS coordinate: the hemisphere letter at refTag
// (pos or neg) and the degrees triple at valTag. It reports false if either is
// missing or malformed, or if the magnitude exceeds limit.
func (r reader) coordinate(gps map[uint16]entry, refTag, valTag uint16, pos, neg byte, limit float64) (float64, bool) {
	ref := r.ascii(gps[refTag])
	if ref == "" || (ref[0] != pos && ref[0] != neg) {
		return 0, false
	}

	v, ok := r.degrees(gps[valTag])
	if !ok || v > limit {
		return 0, false
	}
	// Avoid printing a negative zero for 0 degrees south or west.
	if ref[0] == neg && v != 0 {
		v = -v
	}
	return v, true
}
