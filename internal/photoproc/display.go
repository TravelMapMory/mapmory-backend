package photoproc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"runtime/debug"

	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"

	"github.com/TravelMapMory/mapmory-backend/internal/photometa"
)

const (
	// maxPixels is the largest accepted image, width times height. It admits
	// 48 MP phone cameras and 50 MP Android sensors.
	maxPixels = 50_000_000

	// maxDecodeBytes caps width x height x decodeBytesPerPixel, the memory the
	// standard decoders hold for one image. It admits every baseline YCbCr JPEG
	// and 8-bit PNG up to maxPixels, and keeps one call under about 265 MiB RSS at
	// worst (measured 254 MiB on a smooth image) with a 25 MB input. See "Resource use" in the
	// README.
	maxDecodeBytes = 200_000_000

	// maxDisplayEdge is the longest side of the display image. The contract has
	// one display image for the grid, cover and full view; 2048 px fills a
	// lightbox on a 2x-DPR laptop at about 0.5-0.7 MB. Smaller images are never
	// upscaled.
	maxDisplayEdge = 2048

	// displayQuality is the JPEG quality of the display image.
	displayQuality = 85
)

// DisplayJPEG decodes a JPEG or PNG, scales it so its longest side is at most
// 2048 px, applies the EXIF orientation and re-encodes it as a JPEG. The result
// carries no EXIF, XMP, ICC or GPS data because only pixels are written, and
// data itself is only read, never modified.
//
// Failures caused by the image return an *Error: CodeFileTooLarge,
// CodeUnsupportedType (not a JPEG or PNG, or a JPEG variant the standard
// decoder lacks), CodeImageTooLarge (over maxPixels or maxDecodeBytes, judged
// from the header before any pixels are decoded) or CodeInvalidImage. Any other
// error is internal and not the uploader's fault. Malformed EXIF never fails the
// call; it only means the image is shown unrotated.
//
// One call peaks at about 265 MiB, so callers must run them one at a time.
// It returns the memory to the operating system before it returns.
func DisplayJPEG(data []byte) ([]byte, error) {
	if len(data) > MaxInputBytes {
		return nil, &Error{CodeFileTooLarge, fmt.Errorf("%d bytes exceeds the %d-byte limit", len(data), MaxInputBytes)}
	}

	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	switch {
	case err != nil:
		return nil, decodeFailure(err)
	case format != "jpeg" && format != "png":
		return nil, &Error{CodeUnsupportedType, fmt.Errorf("%s images are not supported", format)}
	case cfg.Width <= 0 || cfg.Height <= 0:
		return nil, &Error{CodeInvalidImage, fmt.Errorf("image is %dx%d", cfg.Width, cfg.Height)}
	// The header check runs before decoding so a decompression bomb is refused cheaply.
	case int64(cfg.Width)*int64(cfg.Height) > maxPixels:
		return nil, &Error{CodeImageTooLarge, fmt.Errorf("%dx%d exceeds the %d-pixel limit", cfg.Width, cfg.Height, maxPixels)}
	case int64(cfg.Width)*int64(cfg.Height)*decodeBytesPerPixel(data, cfg, format) > maxDecodeBytes:
		return nil, &Error{CodeImageTooLarge, fmt.Errorf("%dx%d %s needs more than %d bytes to decode", cfg.Width, cfg.Height, format, maxDecodeBytes)}
	}

	// Back-to-back calls otherwise stack up uncollected images, measured at 660 MiB after three.
	defer debug.FreeOSMemory()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, decodeFailure(err)
	}

	w, h := fitEdge(img.Bounds().Dx(), img.Bounds().Dy())
	dst := resize(img, w, h)

	// Malformed EXIF must not block display, so a parse error means no rotation.
	meta, _ := photometa.Parse(data)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, orient(dst, meta.Orientation), &jpeg.Options{Quality: displayQuality}); err != nil {
		return nil, fmt.Errorf("encode display image: %w", err)
	}
	return buf.Bytes(), nil
}

// decodeFailure classifies an error from image.DecodeConfig or image.Decode.
// An unrecognised signature, or a valid JPEG the standard decoder does not
// implement (arithmetic coding, lossless, 12-bit, unusual chroma subsampling),
// is CodeUnsupportedType; anything else means the file is damaged and is
// CodeInvalidImage.
func decodeFailure(err error) *Error {
	var unsupported jpeg.UnsupportedError
	if errors.Is(err, image.ErrFormat) || errors.As(err, &unsupported) {
		return &Error{CodeUnsupportedType, err}
	}
	return &Error{CodeInvalidImage, err}
}

// decodeBytesPerPixel returns an upper bound on the bytes per pixel that
// image.Decode holds at its peak for data, read from the header that
// image.DecodeConfig already accepted. A JPEG keeps one byte per sample, plus
// four per sample of DCT coefficients when progressive, plus a 4-byte RGBA or
// CMYK copy when it is not YCbCr or gray; YCbCr counts as 4:4:4, except that an
// Adobe RGB marker after the first scan, which only the full decode reads, makes
// it an RGBA copy too (see adobeRGB). A PNG decodes
// to at most 4 bytes per pixel, 8 at 16 bits, doubled when interlaced because
// the passes are decoded into a second image.
func decodeBytesPerPixel(data []byte, cfg image.Config, format string) int64 {
	if format == "png" {
		// Go skips unknown chunks before IHDR, so the fixed offsets hold only when IHDR is first.
		if string(data[12:16]) != "IHDR" {
			return 16
		}
		bpp := int64(4)
		if data[24] == 16 {
			bpp = 8
		}
		if data[28] != 0 {
			bpp *= 2
		}
		return bpp
	}

	samples, converted := int64(3), int64(0)
	switch cfg.ColorModel {
	case color.GrayModel:
		samples = 1
	case color.RGBAModel:
		converted = 4
	case color.YCbCrModel:
		if adobeRGB(data) {
			converted = 4
		}
	case color.CMYKModel:
		samples, converted = 4, 4
	}
	if jpegProgressive(data) {
		return 5*samples + converted
	}
	return samples + converted
}

// adobeRGB reports whether data holds an Adobe APP14 segment with transform 0,
// which makes image/jpeg convert a 3-component image to RGBA. image.DecodeConfig
// stops at the first scan, so it misses such a segment placed after it. FF EE
// cannot occur inside entropy-coded data, so a match is a real marker or stray
// bytes elsewhere, which only make the estimate stricter.
func adobeRGB(data []byte) bool {
	for i := 0; ; i++ {
		j := bytes.Index(data[i:], []byte{0xFF, 0xEE})
		if j < 0 {
			return false
		}
		i += j
		if len(data) >= i+16 && string(data[i+4:i+9]) == "Adobe" && data[i+15] == 0 {
			return true
		}
	}
}

// jpegProgressive reports whether a JPEG's frame is progressive (SOF2). It
// follows the marker segments from the start of the file to the first
// start-of-frame marker, as the standard decoder does. Anything it does not
// follow, such as bytes between segments, counts as progressive, the costlier
// answer, so a crafted header can only make the image look more expensive.
func jpegProgressive(data []byte) bool {
	for i := 2; i+4 <= len(data); {
		switch m := data[i+1]; {
		case data[i] != 0xFF, m == 0x00, 0xD0 <= m && m <= 0xD7, m == 0xC2:
			return true
		case m == 0xFF:
			i++
		case m == 0xC0, m == 0xC1:
			return false
		default:
			i += 2 + int(binary.BigEndian.Uint16(data[i+2:]))
		}
	}
	return true
}

// resize returns img scaled to w x h with Catmull-Rom and composited over
// white, because JPEG has no alpha. It calls Kernel.Transform rather than
// Kernel.Scale: the pixels are the same, but Scale allocates a w x img-height x
// 32-byte buffer (about 200 MB for a 12 MP photo) and Transform none. At equal
// size the result is an exact copy, so no-resize needs no special case.
func resize(img image.Image, w, h int) *image.RGBA {
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	// Transform scales about 4x slower than Scale; a row-streaming separable Catmull-Rom is the upgrade if CPU becomes the limit.
	sx, sy := float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy())
	draw.CatmullRom.Transform(dst, f64.Aff3{sx, 0, -sx * float64(b.Min.X), 0, sy, -sy * float64(b.Min.Y)}, img, b, draw.Over, nil)
	return dst
}

// fitEdge returns the size an image of w by h pixels is scaled to so its longest
// side is at most maxDisplayEdge, keeping the aspect ratio. Images already small
// enough keep their size. Scaling happens before orientation, which is valid
// because a rotation swaps the sides of a bounding box that stays within the
// same square.
func fitEdge(w, h int) (int, int) {
	long := max(w, h)
	if long <= maxDisplayEdge {
		return w, h
	}
	return max(1, (w*maxDisplayEdge+long/2)/long), max(1, (h*maxDisplayEdge+long/2)/long)
}

// orient returns src transformed so it displays upright for EXIF orientation o
// (1-8, as defined by the EXIF standard). Values outside 2-8, including 0 for
// missing, return src unchanged.
func orient(src *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return src
	}

	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			sx, sy := x, y
			switch o {
			case 2:
				sx = w - 1 - x
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sy = h - 1 - y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			dst.SetRGBA(x, y, src.RGBAAt(sx, sy))
		}
	}
	return dst
}
