package photoproc

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"runtime"
	"testing"

	"golang.org/x/image/draw"

	"github.com/TravelMapMory/mapmory-backend/internal/photometa"
)

// decodeOutput decodes DisplayJPEG output and checks it is a JPEG.
func decodeOutput(t *testing.T, out []byte) image.Image {
	t.Helper()

	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" {
		t.Fatalf("output format = %q, want jpeg", format)
	}
	return img
}

// outputSize returns the width and height of DisplayJPEG output.
func outputSize(t *testing.T, out []byte) (int, int) {
	t.Helper()

	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height
}

// TestDisplayJPEGStripsMetadata checks the output is a JPEG with no EXIF or GPS and that the input bytes are left untouched.
func TestDisplayJPEGStripsMetadata(t *testing.T) {
	for _, name := range []string{"gps.jpg", "gps.png"} {
		t.Run(name, func(t *testing.T) {
			in := readFixture(t, name)
			orig := bytes.Clone(in)

			out, err := DisplayJPEG(in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ExtractMetadata(in); err != nil {
				t.Fatal(err)
			}

			decodeOutput(t, out)
			meta, err := photometa.Parse(out)
			if err != nil {
				t.Fatal(err)
			}
			if meta.HasEXIF || meta.GPS != nil || meta.DateTimeOriginal != "" {
				t.Errorf("output still carries metadata: %+v", meta)
			}
			if bytes.Contains(out, []byte("Exif")) {
				t.Error("output contains an Exif marker")
			}
			if !bytes.Equal(in, orig) {
				t.Error("the input bytes were modified")
			}
		})
	}
}

// TestDisplayJPEGOrientation checks each oriented fixture ends up with its dark
// quadrant where an upright viewer would see it.
func TestDisplayJPEGOrientation(t *testing.T) {
	tests := []struct {
		file          string
		width, height int
		darkX, darkY  int
	}{
		{"orientation_3.jpg", 64, 32, 1, 1},
		{"orientation_6.jpg", 32, 64, 1, 0},
		{"orientation_8.jpg", 32, 64, 0, 1},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			out, err := DisplayJPEG(readFixture(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			img := decodeOutput(t, out)

			b := img.Bounds()
			if b.Dx() != tt.width || b.Dy() != tt.height {
				t.Fatalf("size = %dx%d, want %dx%d", b.Dx(), b.Dy(), tt.width, tt.height)
			}

			dark := 0
			for qy := range 2 {
				for qx := range 2 {
					cx, cy := b.Dx()/4+qx*b.Dx()/2, b.Dy()/4+qy*b.Dy()/2
					isDark := color.GrayModel.Convert(img.At(cx, cy)).(color.Gray).Y < 128
					if isDark {
						dark++
					}
					if isDark != (qx == tt.darkX && qy == tt.darkY) {
						t.Errorf("quadrant (%d,%d) dark = %v", qx, qy, isDark)
					}
				}
			}
			if dark != 1 {
				t.Errorf("found %d dark quadrants, want 1", dark)
			}
		})
	}
}

// TestOrient checks every EXIF orientation value against a labelled 3x2 image.
func TestOrient(t *testing.T) {
	// A 3x2 image labelled a b c / d e f, one letter per distinct red value.
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for i, c := range "abcdef" {
		src.SetRGBA(i%3, i/3, color.RGBA{R: uint8(c), A: 255})
	}

	tests := []struct {
		o      int
		width  int
		height int
		want   string
	}{
		{0, 3, 2, "abcdef"},
		{1, 3, 2, "abcdef"},
		{2, 3, 2, "cbafed"},
		{3, 3, 2, "fedcba"},
		{4, 3, 2, "defabc"},
		{5, 2, 3, "adbecf"},
		{6, 2, 3, "daebfc"},
		{7, 2, 3, "fcebda"},
		{8, 2, 3, "cfbead"},
		{9, 3, 2, "abcdef"},
	}

	for _, tt := range tests {
		got := orient(src, tt.o)
		if got.Bounds().Dx() != tt.width || got.Bounds().Dy() != tt.height {
			t.Errorf("orient(%d) size = %dx%d, want %dx%d", tt.o, got.Bounds().Dx(), got.Bounds().Dy(), tt.width, tt.height)
			continue
		}

		var letters []byte
		for y := range tt.height {
			for x := range tt.width {
				letters = append(letters, got.RGBAAt(x, y).R)
			}
		}
		if string(letters) != tt.want {
			t.Errorf("orient(%d) = %s, want %s", tt.o, letters, tt.want)
		}
	}
}

// TestFitEdge checks the scaling arithmetic, including tall, wide and tiny images.
func TestFitEdge(t *testing.T) {
	tests := []struct{ w, h, wantW, wantH int }{
		{8, 8, 8, 8},
		{2048, 1000, 2048, 1000},
		{4032, 3024, 2048, 1536},
		{3024, 4032, 1536, 2048},
		{3000, 1000, 2048, 683},
		{1, 10000, 1, 2048},
		{10000, 1, 2048, 1},
	}

	for _, tt := range tests {
		if w, h := fitEdge(tt.w, tt.h); w != tt.wantW || h != tt.wantH {
			t.Errorf("fitEdge(%d, %d) = %d, %d; want %d, %d", tt.w, tt.h, w, h, tt.wantW, tt.wantH)
		}
	}
}

// TestDisplayJPEGSize checks large images shrink to 2048 px, small ones are not upscaled and unreadable EXIF does not block rendering.
func TestDisplayJPEGSize(t *testing.T) {
	var big bytes.Buffer
	if err := png.Encode(&big, image.NewGray(image.Rect(0, 0, 3000, 1000))); err != nil {
		t.Fatal(err)
	}

	corrupt := readFixture(t, "gps.jpg")
	copy(corrupt[12:14], "XX")

	tests := []struct {
		name          string
		data          []byte
		width, height int
	}{
		{"downscaled to 2048 px", big.Bytes(), 2048, 683},
		{"never upscaled", readFixture(t, "no_exif.jpg"), 8, 8},
		{"unreadable EXIF still renders", corrupt, 8, 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := DisplayJPEG(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if w, h := outputSize(t, out); w != tt.width || h != tt.height {
				t.Errorf("size = %dx%d, want %dx%d", w, h, tt.width, tt.height)
			}
		})
	}
}

// TestDisplayJPEGFlattensAlpha checks a fully transparent PNG becomes white, not black.
func TestDisplayJPEGFlattensAlpha(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}

	out, err := DisplayJPEG(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if y := color.GrayModel.Convert(decodeOutput(t, out).At(2, 2)).(color.Gray).Y; y < 250 {
		t.Errorf("transparent pixel luma = %d, want white", y)
	}
}

// TestDisplayJPEGErrors checks each way an image can be refused maps to its error code and yields no output.
func TestDisplayJPEGErrors(t *testing.T) {
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black}), nil); err != nil {
		t.Fatal(err)
	}

	gpsJPG, gpsPNG := readFixture(t, "gps.jpg"), readFixture(t, "gps.png")
	tooLarge := append(readFixture(t, "no_exif.jpg"), make([]byte, MaxInputBytes)...)

	// patchSOF rewrites the marker and dimensions of the JPEG start-of-frame header.
	patchSOF := func(marker byte, width, height uint16) []byte {
		d := readFixture(t, "no_exif.jpg")
		i := bytes.Index(d, []byte{0xFF, 0xC0})
		d[i+1] = marker
		binary.BigEndian.PutUint16(d[i+5:], height)
		binary.BigEndian.PutUint16(d[i+7:], width)
		return d
	}

	twelveBit := patchSOF(0xC0, 8, 8)
	twelveBit[bytes.Index(twelveBit, []byte{0xFF, 0xC0})+4] = 12

	tests := []struct {
		name string
		data []byte
		code string
	}{
		{"nil", nil, CodeUnsupportedType},
		{"not an image", []byte("not an image"), CodeUnsupportedType},
		{"gif", gifBuf.Bytes(), CodeUnsupportedType},
		{"arithmetic-coded jpeg", patchSOF(0xC9, 8, 8), CodeUnsupportedType},
		{"lossless jpeg", patchSOF(0xC3, 8, 8), CodeUnsupportedType},
		{"12-bit jpeg", twelveBit, CodeUnsupportedType},
		{"jpeg cut inside the header", gpsJPG[:40], CodeInvalidImage},
		{"jpeg cut inside the pixel data", gpsJPG[:len(gpsJPG)-20], CodeInvalidImage},
		{"png cut inside the pixel data", gpsPNG[:len(gpsPNG)-20], CodeInvalidImage},
		{"over the file size limit", tooLarge, CodeFileTooLarge},
		{"over the pixel limit", patchSOF(0xC0, 8192, 8192), CodeImageTooLarge},
		{"progressive gray over the decode budget", patchSOF(0xC2, 6500, 6500), CodeImageTooLarge},
		{"baseline of the same size passes the budget", patchSOF(0xC0, 6500, 6500), CodeInvalidImage},
		{"zero height", patchSOF(0xC0, 8, 0), CodeInvalidImage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := DisplayJPEG(tt.data)
			if got := codeOf(err); got != tt.code {
				t.Errorf("error = %v, want code %q", err, tt.code)
			}
			if out != nil {
				t.Errorf("output has %d bytes on failure, want none", len(out))
			}
		})
	}
}

// TestDecodeBytesPerPixel checks the decode-memory estimate for each header class
// against what the standard decoders allocate.
func TestDecodeBytesPerPixel(t *testing.T) {
	encode := func(img image.Image, f func(*bytes.Buffer, image.Image) error) []byte {
		var buf bytes.Buffer
		if err := f(&buf, img); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	jpg := func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) }
	pngEnc := func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) }
	r := image.Rect(0, 0, 8, 8)

	// progressive turns a baseline JPEG's SOF0 into SOF2; only the header is read.
	progressive := func(d []byte) []byte {
		d = bytes.Clone(d)
		d[bytes.Index(d, []byte{0xFF, 0xC0})+1] = 0xC2
		return d
	}
	// interlaced sets the PNG IHDR interlace byte; only the header is read.
	interlaced := func(d []byte) []byte {
		d = bytes.Clone(d)
		d[28] = 1
		return d
	}
	// withCOM inserts a comment segment holding SOF-like bytes right after SOI.
	withCOM := func(d []byte) []byte {
		com := []byte{0xFF, 0xFE, 0x00, 0x06, 0xFF, 0xC2, 0xFF, 0xC0}
		return append(append(bytes.Clone(d[:2]), com...), d[2:]...)
	}

	ycc := encode(image.NewRGBA(r), jpg)
	// adobeRGBAfterScan puts an Adobe APP14 segment with transform 0 (RGB) just before EOI, past where DecodeConfig stops.
	adobeRGBAfterScan := append(bytes.Clone(ycc[:len(ycc)-2]), 0xFF, 0xEE, 0, 14, 'A', 'd', 'o', 'b', 'e', 0, 100, 0, 0, 0, 0, 0, 0xFF, 0xD9)
	gray := encode(image.NewGray(r), jpg)
	png8 := encode(image.NewNRGBA(r), pngEnc)
	png16 := encode(image.NewNRGBA64(r), pngEnc)
	chunkFirst := append(bytes.Clone(png8[:8]), append([]byte{0, 0, 0, 0, 't', 'E', 'X', 't', 0, 0, 0, 0}, png8[8:]...)...)

	tests := []struct {
		name   string
		data   []byte
		format string
		model  color.Model
		want   int64
	}{
		{"baseline YCbCr JPEG", ycc, "jpeg", color.YCbCrModel, 3},
		{"progressive YCbCr JPEG", progressive(ycc), "jpeg", color.YCbCrModel, 15},
		{"Adobe RGB marker after the scan", adobeRGBAfterScan, "jpeg", color.YCbCrModel, 7},
		{"progressive Adobe RGB marker after the scan", progressive(adobeRGBAfterScan), "jpeg", color.YCbCrModel, 19},
		{"baseline gray JPEG", gray, "jpeg", color.GrayModel, 1},
		{"progressive gray JPEG", progressive(gray), "jpeg", color.GrayModel, 5},
		{"baseline RGB JPEG", ycc, "jpeg", color.RGBAModel, 7},
		{"baseline CMYK JPEG", ycc, "jpeg", color.CMYKModel, 8},
		{"progressive CMYK JPEG", progressive(ycc), "jpeg", color.CMYKModel, 24},
		{"SOF bytes inside an earlier segment", withCOM(ycc), "jpeg", color.YCbCrModel, 3},
		{"extraneous byte before SOF", append([]byte{0xFF, 0xD8, 0x00}, ycc[2:]...), "jpeg", color.YCbCrModel, 15},
		{"8-bit PNG", png8, "png", color.NRGBAModel, 4},
		{"interlaced 8-bit PNG", interlaced(png8), "png", color.NRGBAModel, 8},
		{"16-bit PNG", png16, "png", color.NRGBA64Model, 8},
		{"interlaced 16-bit PNG", interlaced(png16), "png", color.NRGBA64Model, 16},
		{"PNG chunk before IHDR", chunkFirst, "png", color.NRGBAModel, 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := image.Config{ColorModel: tt.model, Width: 8, Height: 8}
			if got := decodeBytesPerPixel(tt.data, cfg, tt.format); got != tt.want {
				t.Errorf("decodeBytesPerPixel = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestResizeMatchesScale checks resize gives exactly the pixels of
// CatmullRom.Scale, including partial alpha and bounds that do not start at zero.
func TestResizeMatchesScale(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	big := image.NewNRGBA(image.Rect(0, 0, 530, 320))
	for i := range big.Pix {
		big.Pix[i] = uint8(rng.IntN(256))
	}
	src := big.SubImage(image.Rect(7, 9, 523, 311))

	want := image.NewRGBA(image.Rect(0, 0, 200, 117))
	draw.Draw(want, want.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(want, want.Bounds(), src, src.Bounds(), draw.Over, nil)

	if got := resize(src, 200, 117); !bytes.Equal(got.Pix, want.Pix) {
		t.Error("resize differs from CatmullRom.Scale")
	}
}

// TestDisplayJPEGMemory guards the memory fix. Scaling must not allocate a
// buffer proportional to the source height, as CatmullRom.Scale does (2048 x
// 2048 x 32 bytes = 134 MB here, against 16 MB for the decoded and display
// images), and the call must collect its garbage before returning.
func TestDisplayJPEGMemory(t *testing.T) {
	var src bytes.Buffer
	if err := png.Encode(&src, image.NewGray(image.Rect(0, 0, 4096, 2048))); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := DisplayJPEG(src.Bytes()); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)

	if got := after.TotalAlloc - before.TotalAlloc; got > 48<<20 {
		t.Errorf("DisplayJPEG allocated %d MiB, want under 48", got>>20)
	}
	if got := int64(after.HeapInuse) - int64(before.HeapInuse); got > 4<<20 {
		t.Errorf("DisplayJPEG left %d MiB of heap in use, want its garbage collected", got>>20)
	}
}
