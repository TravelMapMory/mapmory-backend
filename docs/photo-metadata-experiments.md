# Photo Metadata Experiments

Related: backend issue #13; backend PR #27

## Purpose

Evaluate the standalone `photometa` command as an exploratory aid for photo metadata, geolocation modelling, and upload diagnostics. These experiments do **not** qualify the parser for production image processing.

## Why a custom tool instead of ExifTool?

ExifTool is a mature utility with extensive support for image formats and metadata standards. It is well suited for inspecting photographs and serves as an independent reference for validating our results.

For this exploratory task, a small Go-based inspection tool offers a way to investigate metadata handling within our backend's language and tooling, rather than relying exclusively on an external utility.

The primary motivations are:

- **Domain exploration:** Implementing a limited parser helps us investigate how image metadata is represented, which fields may be missing or malformed, and what assumptions our photo and geolocation models need to handle.
- **Backend compatibility:** A Go implementation allows us to experiment with metadata extraction and checksum calculation within the same language and tooling as the backend, without requiring an external executable.
- **Controlled scope:** Supporting a small set of fields lets us focus on questions relevant to MapMory, such as GPS coordinates, capture timestamps, UTC offsets, file identity, and diagnostic failures.
- **Extensibility:** The tool provides a starting point for future upload diagnostics, including comparing local and uploaded file checksums or investigating unexpected metadata.

### Tradeoffs and limitations

This approach comes with additional implementation and maintenance responsibilities. Our parser supports substantially fewer formats and metadata variations than ExifTool, and custom binary parsing introduces correctness and robustness risks.

We therefore use **ExifTool as an independent reference**, not as a competitor that our implementation is intended to replace.

The current tool is exploratory and should not be assumed suitable for production image processing. Before reusing it in the upload pipeline, we should evaluate whether maintaining the custom parser offers sufficient advantages over established Go libraries or external tooling.

The objective is to learn about our data and upload requirements, not to build a general-purpose replacement for ExifTool.

## Reproduction

From the backend repository root, on branch `feat/photo-metadata-script`:

```bash
go test ./...
```

To inspect a real iPhone original and a JPEG made from it (requires macOS `sips` and `exiftool`):

```bash
ORIGINAL="/path/to/original.HEIC"
CONVERTED="/tmp/photo-converted.jpg"

exiftool -a -G1 -s -FileType -DateTimeOriginal -OffsetTimeOriginal \
  -GPSLatitude -GPSLongitude "$ORIGINAL"
go run ./cmd/photometa "$ORIGINAL"
sips -s format jpeg "$ORIGINAL" --out "$CONVERTED"
exiftool -a -G1 -s -DateTimeOriginal -OffsetTimeOriginal \
  -GPSLatitude -GPSLongitude "$CONVERTED"
go run ./cmd/photometa "$CONVERTED"
shasum -a 256 "$CONVERTED"
```

> [!NOTE]
> The HEIC invocation intentionally demonstrates unsupported-format handling and is expected to exit unsuccessfully. The subsequent JPEG conversion tests whether the metadata can be extracted from a supported representation.

Do not commit personal photo originals, location-bearing EXIF, or precise coordinates to the public repository.

## Bundled test fixtures

The five fixtures below live in `internal/photometa/testdata/`. They are small, synthetic inputs intended to exercise specific metadata conditions; they are **not representative samples of phone-camera image compatibility**.

| Fixture | Scenario being tested | Expected CLI observations |
| --- | --- | --- |
| `gps.jpg` | JPEG with valid EXIF GPS and capture timestamp, including a UTC offset | `format: jpeg`, `exif: present`, latitude `60.1868333`, longitude `24.8278000`, `DateTimeOriginal: "2025:07:14 09:30:00"`, `OffsetTimeOriginal: "+03:00"` |
| `gps.png` | PNG with an EXIF `eXIf` chunk and coordinates in the southern and western hemispheres | `format: png`, `exif: present`, latitude `-22.9516667`, longitude `-43.2105000`; capture timestamp and offset both missing |
| `no_gps.jpg` | JPEG with EXIF capture time but no GPS and no timezone offset | `format: jpeg`, `exif: present`, GPS coordinates missing, `DateTimeOriginal: "2025:07:15 18:05:42"`, offset missing |
| `no_exif.jpg` | JPEG without an EXIF block | `format: jpeg`, `exif: missing`; GPS, capture timestamp, and offset all missing |
| `bad_gps_offset.jpg` | JPEG with malformed GPS metadata but otherwise readable time fields | `format: jpeg`, `exif: present`, GPS coordinates missing; `DateTimeOriginal: "2025:07:14 09:30:00"` and offset `"+03:00"` preserved |

For a deterministic run, list the fixtures explicitly (rather than relying on shell glob ordering):

```bash
go run ./cmd/photometa \
  internal/photometa/testdata/gps.jpg \
  internal/photometa/testdata/gps.png \
  internal/photometa/testdata/no_gps.jpg \
  internal/photometa/testdata/no_exif.jpg \
  internal/photometa/testdata/bad_gps_offset.jpg
```

**Interpretation:** `no_gps.jpg` distinguishes missing location from missing EXIF entirely; `bad_gps_offset.jpg` checks that malformed GPS does not discard valid time fields. The negative coordinates in `gps.png` verify correct hemisphere signs. The successful outputs above were observed when these fixtures were run during review.

## Observations (17 October 2026)

| Experiment | Observed result | Status |
| --- | --- | --- |
| Original iPhone 14 Pro Max HEIC | ExifTool found a capture timestamp, UTC offset, and GPS fields; `photometa` returned `not a JPEG or PNG file` with exit status 1 | Verified |
| HEIC converted to JPEG with macOS `sips` | ExifTool found that capture timestamp, offset, and GPS survived this particular conversion | Verified |
| Converted JPEG with `photometa` | Reported `format: jpeg`, `size_bytes: 3043221`, `exif: present`, and matching GPS, timestamp, and UTC-offset fields; produced SHA-256 `2acfcc1562d036d31dee860dce9d43b9c7692b7cb6a941de2943b1dbc0f16d44` | Verified |
| Independent checksum comparison | macOS `shasum -a 256` exactly matched the converted JPEG digest reported by the CLI (`2acfcc15…f16d44`) | Verified |
| Automated Go tests and synthetic EXIF fixtures | `go test ./...` passed for all three packages. `gps.jpg`, `gps.png`, `no_gps.jpg`, `no_exif.jpg`, and `bad_gps_offset.jpg` returned expected GPS, timestamp, and missing/invalid-field behavior | Verified |
| Identical copies, renamed file, and one-byte modification | Two copies of the converted JPEG retained its checksum and metadata, including when renamed `.jpeg`. Appending one zero byte increased size from 3,043,221 to 3,043,222 bytes, changed SHA-256 (`a1159535…d98`), and left EXIF readable | Verified |
| Missing or unsupported file in a batch | Missing path and text file produced distinct errors, but subsequent valid JPEG was still inspected; process exited `1`. No-argument invocation displayed usage and exited `2` | Verified |

The precise GPS values are intentionally omitted. The source photo was captured on 15 October 2026 and its timezone offset was `+03:00`; the converted JPEG retained both fields.

## Additional reproducible checks

Use the converted JPEG from the reproduction section. The following commands create only disposable test copies:

```bash
TEST_DIR="$(mktemp -d)"
cp "$CONVERTED" "$TEST_DIR/identical.jpg"
cp "$CONVERTED" "$TEST_DIR/renamed.jpeg"
cp "$CONVERTED" "$TEST_DIR/modified.jpg"
printf '\000' >> "$TEST_DIR/modified.jpg"

shasum -a 256 \
  "$CONVERTED" \
  "$TEST_DIR/identical.jpg" \
  "$TEST_DIR/renamed.jpeg" \
  "$TEST_DIR/modified.jpg"

go run ./cmd/photometa "$TEST_DIR/identical.jpg" \
  "$TEST_DIR/renamed.jpeg" "$TEST_DIR/modified.jpg"

printf 'not a real image\n' > "$TEST_DIR/not-image.txt"
go build -o "$TEST_DIR/photometa" ./cmd/photometa
"$TEST_DIR/photometa" "$TEST_DIR/identical.jpg" \
  "$TEST_DIR/missing.jpg" "$TEST_DIR/not-image.txt" "$TEST_DIR/renamed.jpeg"
echo "Mixed-batch exit code: $?"  # Expected: 1
"$TEST_DIR/photometa"
echo "No-arguments exit code: $?"  # Expected: 2
rm -r "$TEST_DIR"
```

The Go test suite and bundled fixtures were independently run during review. No unexpected behavior was observed in the tests above. The three separately shared JPEG copies have not been run through the CLI in this verification session; the original observations about their missing EXIF were made through independent file inspection.

## Implications for the MapMory domain model

1. **Format versus metadata:** A legitimate phone original may be HEIC and contain useful EXIF despite being unsupported by the current JPEG/PNG-only CLI. Format support should be tracked separately from metadata semantics.
2. **Original versus derivative:** A conversion can preserve EXIF, but this is not guaranteed for every export process. Prefer inspecting the original uploaded bytes, not assuming metadata survives in display images or thumbnails.
3. **Missing versus invalid versus corrected location:** These are different states. A photo with no GPS remains valid; immutable source coordinates should remain separate from effective user-corrected location.
4. **Capture-time uncertainty:** `DateTimeOriginal` and `OffsetTimeOriginal` are distinct source fields. Not every image supplies an offset; conversion to UTC must account for missing offsets rather than invent one.
5. **Checksum scope:** SHA-256 identifies byte-for-byte identical files, not visually identical photos. Original, display image, and thumbnail may legitimately have different checksums.

## Scope and possible extensions

The current CLI is an experimental, local-file diagnostic, not a production image validator. Its documented format support is JPEG and PNG (including supported EXIF representations); HEIC is not currently supported. A future checksum/debugging helper could compare local originals with downloaded GCS objects, report differences between original and derivative files, and support additional formats such as HEIC. Track such capabilities separately rather than expanding the acceptance criteria of backend #13 retroactively.

## Remaining review checks

- [x] Compare the converted JPEG SHA-256 with `shasum -a 256`.
- [x] Independently run `go test ./...`.
- [x] Run the bundled EXIF fixtures and a missing/invalid-input case.
- [x] Record findings and check for unexpected behavior before approving PR #27 (none identified in the completed checks).
