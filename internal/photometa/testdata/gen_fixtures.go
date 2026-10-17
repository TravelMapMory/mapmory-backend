//go:build ignore

// gen_fixtures writes the synthetic test images used by the photometa tests
// into internal/photometa/testdata. Every file is a tiny 8x8 grey image with a
// hand-built EXIF block, so no real photo (and no real location) is committed.
//
// Run it from the repository root:
//
//	go run ./internal/photometa/testdata/gen_fixtures.go
//
// image/jpeg and image/png output is not guaranteed byte-stable across Go
// versions, so regenerating may change the files; the tests do not depend on
// their exact bytes.
package main

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"slices"
)

const outDir = "internal/photometa/testdata"

// TIFF value types used by the fixtures.
const (
	typeASCII    = 2
	typeLong     = 4
	typeRational = 5
)

// entry is one IFD entry. val holds the encoded value; encodeIFD stores it
// inline when it is at most 4 bytes and out of line otherwise.
type entry struct {
	tag, typ uint16
	count    uint32
	val      []byte
}

// asciiEntry returns an ASCII entry holding s plus the terminating NUL.
func asciiEntry(tag uint16, s string) entry {
	return entry{tag: tag, typ: typeASCII, count: uint32(len(s) + 1), val: []byte(s + "\x00")}
}

// longEntry returns a single-value LONG entry.
func longEntry(bo binary.AppendByteOrder, tag uint16, v uint32) entry {
	return entry{tag: tag, typ: typeLong, count: 1, val: bo.AppendUint32(nil, v)}
}

// rationalEntry returns a RATIONAL entry from alternating numerator and
// denominator values.
func rationalEntry(bo binary.AppendByteOrder, tag uint16, numDen ...uint32) entry {
	var val []byte
	for _, v := range numDen {
		val = bo.AppendUint32(val, v)
	}
	return entry{tag: tag, typ: typeRational, count: uint32(len(numDen) / 2), val: val}
}

// exifEntries returns the Exif IFD entries for a capture time and an optional
// UTC offset ("" leaves OffsetTimeOriginal out).
func exifEntries(dateTime, offset string) []entry {
	entries := []entry{asciiEntry(0x9003, dateTime)}
	if offset != "" {
		entries = append(entries, asciiEntry(0x9011, offset))
	}
	return entries
}

// gpsEntries returns the four GPS IFD entries for a latitude and longitude,
// each given as hemisphere letter plus degrees, minutes and seconds rationals.
func gpsEntries(bo binary.AppendByteOrder, latRef string, lat []uint32, lngRef string, lng []uint32) []entry {
	return []entry{
		asciiEntry(0x0001, latRef),
		rationalEntry(bo, 0x0002, lat...),
		asciiEntry(0x0003, lngRef),
		rationalEntry(bo, 0x0004, lng...),
	}
}

// encodeIFD serialises a directory that starts at offset base of the TIFF
// block: the sorted entry table, a zero next-IFD link, then the out-of-line
// values padded to even length so following offsets stay word-aligned.
func encodeIFD(bo binary.AppendByteOrder, base uint32, entries []entry) []byte {
	slices.SortFunc(entries, func(a, b entry) int { return int(a.tag) - int(b.tag) })

	table := bo.AppendUint16(nil, uint16(len(entries)))
	var extra []byte
	extraBase := base + 2 + 12*uint32(len(entries)) + 4
	for _, e := range entries {
		table = bo.AppendUint16(table, e.tag)
		table = bo.AppendUint16(table, e.typ)
		table = bo.AppendUint32(table, e.count)

		if len(e.val) <= 4 {
			table = append(table, e.val...)
			table = append(table, make([]byte, 4-len(e.val))...)
			continue
		}
		table = bo.AppendUint32(table, extraBase+uint32(len(extra)))
		extra = append(extra, e.val...)
		if len(extra)%2 == 1 {
			extra = append(extra, 0)
		}
	}
	table = bo.AppendUint32(table, 0)
	return append(table, extra...)
}

// buildTIFF assembles a TIFF block: header, IFD0 holding only the pointers to
// the sub-directories that have entries, then the Exif IFD, then the GPS IFD.
func buildTIFF(bo binary.AppendByteOrder, exif, gps []entry) []byte {
	pointers := 0
	if exif != nil {
		pointers++
	}
	if gps != nil {
		pointers++
	}

	mark := "II"
	if bo == binary.BigEndian {
		mark = "MM"
	}
	block := bo.AppendUint16([]byte(mark), 42)
	block = bo.AppendUint32(block, 8)

	ifd0Len := uint32(2 + 12*pointers + 4)
	exifOff := 8 + ifd0Len
	var exifIFD []byte
	if exif != nil {
		exifIFD = encodeIFD(bo, exifOff, exif)
	}
	gpsOff := exifOff + uint32(len(exifIFD))

	var ifd0 []entry
	if exif != nil {
		ifd0 = append(ifd0, longEntry(bo, 0x8769, exifOff))
	}
	if gps != nil {
		ifd0 = append(ifd0, longEntry(bo, 0x8825, gpsOff))
	}
	block = append(block, encodeIFD(bo, 8, ifd0)...)
	block = append(block, exifIFD...)
	if gps != nil {
		block = append(block, encodeIFD(bo, gpsOff, gps)...)
	}
	return block
}

// withAPP1 inserts an Exif APP1 segment carrying block right after the JPEG
// SOI marker.
func withAPP1(jpg, block []byte) []byte {
	out := append([]byte{}, jpg[:2]...)
	out = append(out, 0xFF, 0xE1)
	out = binary.BigEndian.AppendUint16(out, uint16(2+6+len(block)))
	out = append(out, "Exif\x00\x00"...)
	out = append(out, block...)
	return append(out, jpg[2:]...)
}

// withEXIf inserts an eXIf chunk carrying block right after the PNG IHDR
// chunk, which always ends at offset 33.
func withEXIf(img, block []byte) []byte {
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(block)))
	chunk = append(chunk, "eXIf"...)
	chunk = append(chunk, block...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))

	out := append([]byte{}, img[:33]...)
	out = append(out, chunk...)
	return append(out, img[33:]...)
}

// baseImage returns the 8x8 uniform grey image all fixtures are built from.
func baseImage() image.Image {
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	return img
}

// encodeJPEG returns baseImage as a plain JPEG with no EXIF.
func encodeJPEG() []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, baseImage(), &jpeg.Options{Quality: 75}); err != nil {
		log.Fatal(err)
	}
	return buf.Bytes()
}

// encodePNG returns baseImage as a plain PNG with no EXIF.
func encodePNG() []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, baseImage()); err != nil {
		log.Fatal(err)
	}
	return buf.Bytes()
}

// fullTIFF builds the "everything present" block: capture time and offset, and
// a position of 60 deg 11' 12.6" N, 24 deg 49' 40.08" E. latSecDen is the
// denominator of the latitude seconds (10 is valid, 0 makes the position unusable).
func fullTIFF(bo binary.AppendByteOrder, latSecDen uint32) []byte {
	gps := gpsEntries(bo, "N", []uint32{60, 1, 11, 1, 126, latSecDen}, "E", []uint32{24, 1, 49, 1, 4008, 100})
	return buildTIFF(bo, exifEntries("2025:07:14 09:30:00", "+03:00"), gps)
}

// main writes all fixtures. Seconds values are multiples of 0.006" (0.0001') so macOS
// ImageIO, which rounds minutes to 4 decimals, agrees with the parser's output.
func main() {
	if _, err := os.Stat(outDir); err != nil {
		log.Fatalf("run this from the repository root: %v", err)
	}

	jpg, img := encodeJPEG(), encodePNG()
	be, le := binary.BigEndian, binary.LittleEndian

	badPointer := fullTIFF(le, 10)
	// Entry 1 of IFD0 is the GPS pointer; its value field is at 8 + 2 + 12 + 8.
	le.PutUint32(badPointer[30:], 0xFFFFFFF0)

	// 22 deg 57' 6" S, 43 deg 12' 37.8" W, and no Exif IFD.
	southWest := gpsEntries(le, "S", []uint32{22, 1, 57, 1, 6, 1}, "W", []uint32{43, 1, 12, 1, 378, 10})

	fixtures := map[string][]byte{
		"gps.jpg":            withAPP1(jpg, fullTIFF(be, 10)),
		"gps.png":            withEXIf(img, buildTIFF(le, nil, southWest)),
		"no_gps.jpg":         withAPP1(jpg, buildTIFF(le, exifEntries("2025:07:15 18:05:42", ""), nil)),
		"no_exif.jpg":        jpg,
		"no_exif.png":        img,
		"zero_den.jpg":       withAPP1(jpg, fullTIFF(le, 0)),
		"bad_gps_offset.jpg": withAPP1(jpg, badPointer),
	}
	for name, data := range fixtures {
		if err := os.WriteFile(filepath.Join(outDir, name), data, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}
