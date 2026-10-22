# MapMory (Backend)

See [Backend coding conventions](docs/coding-conventions.md) for repository expectations covering formatting, naming, error handling, HTTP handlers, and testing.

MapMory is a photo-first travel-memory application. Its planned backend manages trips and photos, processes image metadata (including EXIF coordinates and capture times), and supports location-based browsing and sharing.

This repository contains the **Go REST API**. The current implementation is an early prototype: it provides a health check and basic trip creation and retrieval with **in-memory storage**. Photo processing and persistent storage are not implemented yet. Firebase ID-token verification is implemented for a diagnostic endpoint, but trip authorization is not. The photo processing code exists as a standalone module (see [Photo processing module](#photo-processing-module)).

- [Frontend repository](https://github.com/TravelMapMory/mapmory-frontend)
- [OpenAPI contract](openapi/openapi.yaml) — target API behavior; not every operation is implemented

## Project status

| Capability | Status |
| --- | --- |
| Health check | Implemented |
| Create a trip | Implemented; in-memory |
| List trips | Implemented; no pagination yet |
| Get a trip by ID | Implemented; in-memory |
| Firebase ID token verification | Implemented for `GET /api/auth/me` |
| Database user resolution and trip ownership | Pending |
| PostgreSQL persistence | Pending |
| Photo metadata inspection command | Implemented; local files only |
| EXIF normalization and display images | Implemented in `internal/photoproc`; not yet called by any endpoint |
| Photo registration, uploads, and processing | Pending |
| Location resolution and sharing | Pending |

**Important:** The OpenAPI specification describes authenticated, owner-scoped operations. The prototype verifies Firebase ID tokens on **`/api/auth/me` only**. Trip routes do **not** enforce authentication or ownership; all trips in the server's process-wide memory remain accessible to callers. Do not deploy it as a production service holding private user data.

## Getting started

### Prerequisites

- Go 1.26.1 or newer (as declared in `go.mod`)

### Firebase Authentication (development configuration)

MapMory uses the shared Firebase project **`mapmory-development`**. Everyone can run their own frontend and backend locally. The frontend signs application users in through Firebase and sends a **Firebase ID token** to the API. Google Cloud **ADC service-account impersonation** is a separate way for a local backend to authenticate to Google APIs; its OAuth token is **not** a MapMory user token.

For developer onboarding, see [Firebase Authentication — local development](docs/firebase-auth-setup.md); for sign-in and token tests, see [Firebase Authentication — testing](docs/firebase-auth-testing.md). In the setup guide, **start with the identity explanation**, then follow the teammate access request and keyless `gcloud` setup. Ki Chun currently administers the Firebase project; other teammates can implement frontend sign-in without Firebase Console access. Backend developers request narrowly scoped impersonation access on `mapmory-local-dev` through Google Cloud IAM.

**Status:** `GET /api/auth/me` verifies real Firebase ID tokens and returns the verified UID. The trip endpoints remain unprotected and do not enforce ownership. Never treat in-memory trips as private.

### Run locally

From the repository root, complete the [keyless ADC setup](docs/firebase-auth-setup.md) first, then configure the Firebase project:

```bash
export FIREBASE_PROJECT_ID=mapmory-development
unset GOOGLE_APPLICATION_CREDENTIALS
unset FIREBASE_AUTH_EMULATOR_HOST
go run ./cmd/server
```

The server listens on `http://localhost:8080` by default. Override the port with `PORT`:

```bash
PORT=9090 go run ./cmd/server
```

Trips exist only in memory and are **lost when the server restarts**.

### Verify the API

Check liveness:

```bash
curl -i http://localhost:8080/healthz
```

Create a trip:

```bash
curl -i -X POST http://localhost:8080/api/trips \
  -H 'Content-Type: application/json' \
  -d '{"title":"Japan","notes":"Summer trip"}'
```

The creation request returns `201 Created` with the new trip, including a UUID `id`. Use that `id` in the next request:

```bash
curl -i http://localhost:8080/api/trips/<trip-uuid>
```

List all trips:

```bash
curl -i http://localhost:8080/api/trips
```

Check the protected authentication endpoint (without a Firebase ID token it returns `401`):

```bash
curl -i http://localhost:8080/api/auth/me
```

With a genuine Firebase **user ID token** from the `mapmory-development` Email/Password sign-in flow:

```bash
curl -i http://localhost:8080/api/auth/me \
  -H "Authorization: Bearer $FIREBASE_ID_TOKEN"
```

A valid token returns `200` with `{"uid":"<verified Firebase UID>"}`. See the [Firebase testing guide](docs/firebase-auth-testing.md) for a safe, reproducible real-token test. Never substitute the Google OAuth token from `gcloud auth application-default print-access-token` here.

More examples, including authenticated and rejected requests, trip creation, and invalid inputs, are in [`requests.http`](requests.http). The file supports VS Code's REST Client extension. Follow the [real-token walkthrough](docs/firebase-auth-testing.md) to obtain a Firebase ID token and write it into a local, Git-ignored `.env` file **before clearing the shell variable**. `.env.example` documents `FIREBASE_WEB_API_KEY`, disposable test-user credentials, and the temporary `FIREBASE_ID_TOKEN` used by REST Client. Keep these values in a local, ignored `.env` file, and follow the testing guide to add the token **without overwriting other entries**. Only use disposable test-account passwords, never real credentials. The Go server does not load `.env` automatically; the testing guide reads the Web API key and disposable test credentials explicitly. Remove the ID token from `.env` after testing; never commit real ID tokens.

## Implemented endpoints

| Method | Path | Success | Notes |
| --- | --- | --- | --- |
| `GET` | `/healthz` | `200` | Returns `{"status":"ok"}` |
| `GET` | `/api/auth/me` | `200` | Requires a verified Firebase ID token; returns `{"uid":"..."}`. Missing/invalid token → `401` |
| `GET` | `/api/trips` | `200` | Returns `{ "items": [...], "next_cursor": null }`; pagination not yet implemented |
| `POST` | `/api/trips` | `201` | Creates a trip from `title` and optional `notes` |
| `GET` | `/api/trips/{trip_id}` | `200` | Returns a trip; missing or malformed IDs return `404` |

A newly created trip has `photo_count: 0` and `cover: null`. Trip titles are trimmed and must be nonblank and at most 120 Unicode characters. Invalid creation requests return `400` with `error.code: "invalid_request"`.

Missing trips return JSON rather than a plain-text router error:

```json
{
  "error": {
    "code": "not_found",
    "message": "trip not found"
  }
}
```

The authentication diagnostic route implements `401` for missing or invalid Firebase tokens. **Trip routes do not yet implement** the contract’s `401` authentication or owner-scoped `404` behavior.

## Code structure

| Path | Responsibility |
| --- | --- |
| `cmd/server/main.go` | HTTP server, routes, request handlers, protected diagnostic endpoint |
| `cmd/server/auth_route_test.go` | Route-level authentication tests with an injected fake verifier |
| `internal/auth/` | Firebase Admin SDK initialization, bearer middleware, verified identity in request context, unit tests |
| `cmd/server/api_types.go` | JSON request and response types |
| `cmd/server/trip_store.go` | Thread-safe in-memory trip storage |
| `cmd/server/*_test.go` | Handler, storage, and API model tests |
| `cmd/photometa/main.go` | Photo metadata inspection command |
| `internal/photometa/photometa.go` | JPEG and PNG EXIF reader used by the command and `internal/photoproc` |
| `internal/photometa/testdata/` | Synthetic test images and the generator that writes them |
| `internal/photoproc/photoproc.go` | Metadata normalization (GPS, capture time, UTC offset) and processing errors |
| `internal/photoproc/display.go` | EXIF-free display JPEG: size limits, scaling, orientation |
| `openapi/openapi.yaml` | Proposed API specification |
| `requests.http` | Manual API requests |

The HTTP handlers use Go's standard `net/http` router and structured JSON errors. The in-memory trip store is synchronized, but does not persist or enforce ownership.

### Database migrations

Database schema changes use PostgreSQL 17 and Goose v3.29.0. See [Database migrations](docs/database-migrations.md) for local setup, migration commands, and testing.

## Tests and checks

Run the automated tests:

```bash
go test ./...
```

Run additional checks before opening a PR:

```bash
gofmt -l .
go vet ./...
go build ./...
```

`gofmt -l .` lists files that are not formatted and must print nothing; run `gofmt -w .` to fix them.

CI runs a gofmt check, vet, build, tests, and a Docker image build. These checks do not establish production readiness or test live Firebase, PostgreSQL, or Google Cloud Storage integration. Auth middleware and route tests use a fake verifier and run without Firebase credentials; a real-token smoke test is documented separately.

## Photo metadata inspection

`photometa` is a standalone exploratory command for inspecting local JPEG and PNG files. It reports file size, SHA-256, EXIF GPS coordinates, and capture timestamps to help investigate image metadata, geolocation modelling, and upload diagnostics ([backend #13](https://github.com/TravelMapMory/mapmory-backend/issues/13)).

The tool is not part of the production upload pipeline.

See [Photo Metadata Experiments](docs/photo-metadata-experiments.md) for reproducible experiments, fixture explanations, real-image observations, and implications for the photo domain model.

```bash
go run ./cmd/photometa <file>...
```

Example, on a GPS-tagged and a no-GPS test image:

```bash
go run ./cmd/photometa internal/photometa/testdata/gps.jpg internal/photometa/testdata/no_gps.jpg
```

```text
file:               internal/photometa/testdata/gps.jpg
format:             jpeg
size_bytes:         582
sha256:             1e6a371b252a316e19124e4b7bbe0ede9addfc9e3cd5e8159d9d8d18c7ea985f
exif:               present
gps_latitude:       60.1868333
gps_longitude:      24.8278000
DateTimeOriginal:   "2025:07:14 09:30:00"
OffsetTimeOriginal: "+03:00"

file:               internal/photometa/testdata/no_gps.jpg
format:             jpeg
size_bytes:         448
sha256:             9b7c2e30e0d9e2d4d24534b6040deac3c702ab3c59b7e4b4d73e7e1c29cc7699
exif:               present
gps_latitude:       missing
gps_longitude:      missing
DateTimeOriginal:   "2025:07:15 18:05:42"
OffsetTimeOriginal: missing
```

The expected sizes and checksums correspond to the committed fixtures. They should remain unchanged when regenerated from the same generator unless the fixture contents change.

How to read the output:

- `missing` means the file does not carry that field, or the value is unusable (for example a GPS coordinate with a zero denominator, or one outside the valid range). A missing field is not an error.
- `gps_latitude` and `gps_longitude` are signed decimal degrees displayed to seven decimal places. North and east are positive; south and west are negative. Both coordinates are reported together or marked missing.
- `DateTimeOriginal` and `OffsetTimeOriginal` are the EXIF strings exactly as stored, in quotes. Labels in CamelCase are verbatim EXIF tags; labels in snake_case are derived values.
- `size_bytes` and `sha256` describe the whole file. The digest matches `shasum -a 256 <file>`, so identical bytes give identical output.

Exit status is `0` when every file was read, `1` when any file failed (one `photometa: <file>: <reason>` line per failure on stderr; the other files are still processed), and `2` when no file was given. Under `go run`, any nonzero status is reported as `exit status N`.

### Comparing with a Google Cloud Storage original

Download the object first, then compare the checksums:

```bash
gcloud storage cp gs://<bucket>/<object> ./downloaded-original.jpg

shasum -a 256 source.jpg downloaded-original.jpg
```

Matching digests provide strong evidence that the files are byte-for-byte identical, including their embedded metadata.

To inspect the metadata separately:

```bash
go run ./cmd/photometa source.jpg downloaded-original.jpg
```

This comparison applies to copies of the same original file, not resized or re-encoded derivatives.

### Limits

- Only EXIF in a JPEG APP1 segment or a PNG `eXIf` chunk is read. HEIC, XMP, and legacy PNG text profiles are not.
- The tool is not an image validator: it stops reading once it has the EXIF block.
- The whole file is read into memory.

### Regenerating test fixtures

The images in `internal/photometa/testdata/` are tiny synthetic files with hand-built EXIF, so no real photo or location is committed. Regenerate them from the repository root:

```bash
go run ./internal/photometa/testdata/gen_fixtures.go
```

## Photo processing module

`internal/photoproc` is the standalone component behind the EXIF extraction and display-image work in [backend #22](https://github.com/TravelMapMory/mapmory-backend/issues/22). It works on bytes already in memory, so it needs no HTTP server, cloud credentials, database or geocoder, and it is not called by any endpoint yet. Reading the uploaded object from Google Cloud Storage, saving the `Photo` record and wiring this into upload completion belong to [#12](https://github.com/TravelMapMory/mapmory-backend/issues/12), [#21](https://github.com/TravelMapMory/mapmory-backend/issues/21) and [#23](https://github.com/TravelMapMory/mapmory-backend/issues/23).

### Entry points

| Name | Purpose |
| --- | --- |
| `photoproc.DisplayJPEG(data []byte) ([]byte, error)` | Validates the image and returns the display JPEG |
| `photoproc.ExtractMetadata(data []byte) (Metadata, error)` | Returns GPS, capture time and UTC offset, each marked `missing`, `malformed` or `usable` |
| `photoproc.MaxInputBytes` | Largest accepted upload, 25,000,000 bytes (the OpenAPI limit) |
| `(*photoproc.Error).Code` | Reason an image was refused; safe to store as `Photo.error_code` |

### Input and output

- Input: JPEG or PNG. The format is read from the file's magic bytes, never from the file name or `Content-Type`.
- Output: a baseline JPEG at quality 85. Only pixels are written, so the output carries no EXIF, XMP, ICC profile or GPS data. The input slice is only read; the original is never modified, and the worker must store the display image as a separate object.
- The longest side is at most 2048 px and smaller images are never upscaled. Scaling uses Catmull-Rom (`draw.CatmullRom.Transform`) from `golang.org/x/image`, a pure-Go package, so the `CGO_ENABLED=0` build still works.
- The EXIF orientation is applied to the pixels, so the output is upright without the tag.
- Transparency is flattened onto white, because JPEG has no alpha channel.

### Limits and errors

| Condition | `Error.Code` |
| --- | --- |
| More than 25,000,000 bytes | `file_too_large` |
| Not a JPEG or PNG (for example GIF, WebP, HEIC or text), or a JPEG that Go's decoder does not implement (arithmetic coding, lossless, 12-bit, or a rare chroma subsampling) | `unsupported_type` |
| More than 50 megapixels, or more than the decode budget for its kind of image (see [Resource use](#resource-use)); both checked from the header before any pixels are decoded | `image_too_large` |
| Zero width or height, or header or pixel data that cannot be decoded, including truncated files | `invalid_image` |

`Error.Err` holds the cause for logs and should not be shown to users. Any error from `DisplayJPEG` that is not an `*Error` is internal (for example an encoder failure) and should be treated as a server error.

#### Resource use

Memory is the constraint: the API runs on a GCE e2-micro VM whose 1 GB is shared with Caddy, nginx and later Postgres. Before decoding, `DisplayJPEG` estimates from the header how much memory the standard-library decoder will hold, and refuses the image as `image_too_large` when

```text
width x height x bytes per pixel > 200,000,000
```

| Input | Bytes per pixel while decoding | Largest accepted |
| --- | --- | --- |
| Baseline JPEG, YCbCr / grayscale | 3 / 1 | 50 MP (the pixel limit) |
| Baseline JPEG, RGB / CMYK | 7 / 8 | 28 MP / 25 MP |
| Progressive JPEG, YCbCr | 15 | 13.3 MP |
| Progressive JPEG, grayscale / RGB / CMYK | 5 / 19 / 24 | 40 MP / 10.5 MP / 8.3 MP |
| 8-bit PNG | 4 | 50 MP (the pixel limit) |
| 16-bit PNG, or interlaced 8-bit PNG | 8 | 25 MP |
| Interlaced 16-bit PNG | 16 | 12.5 MP |

The decoder holds one byte per sample, and YCbCr is counted as 4:4:4 because the subsampling is not checked. A progressive JPEG also keeps every DCT coefficient (4 bytes per sample), an RGB or CMYK JPEG is converted into a second image of 4 bytes per pixel, and an interlaced PNG is decoded pass by pass into a second image. A JPEG with an Adobe marker whose transform is 0 is converted from YCbCr to RGB at the end of the decode, so it counts as RGB even when the marker sits after the first scan, where the header read does not look. Phone cameras write baseline YCbCr JPEGs, so their photos are accepted up to 50 MP.

Scaling adds no buffer proportional to the source: `Kernel.Transform` produces exactly the pixels of `CatmullRom.Scale`, which allocated 2048 x the source height x 32 bytes (about 200 MB for a 12 MP photo). Peak memory of one call is therefore about

```text
peak = input bytes + decode (above) + 2 x display image (up to 16 MiB each) + about 8 MiB
```

which stays under about 265 MiB with a 25 MB input, counting the output JPEG while it is encoded (up to ~4 MiB) and a detailed scaled image. The display image is 12 MiB for a 4:3 photo and 16 MiB for a square one. The second display image is the rotated copy, made only when the EXIF orientation is 2 to 8.

Peak RSS of one `DisplayJPEG` call in a fresh process, measured with `/usr/bin/time -l` on a development laptop (Apple M4 Pro) with synthetic photos (indicative, not a benchmark). The JPEGs carry EXIF orientation 6, so the rotated copy is included:

| Input | Peak RSS | Time |
| --- | --- | --- |
| 12 MP baseline JPEG, 4:2:0 (2 MB) | 50 MiB | 1.0 s |
| 50 MP baseline JPEG, 4:2:0 (7.5 MB) | 110 MiB | 3.1 s |
| 50 MP baseline JPEG, 4:4:4 (23 MB) | 197 MiB | 3.5 s |
| 13.3 MP progressive JPEG, 4:4:4 (2.6 MB) | 222 MiB | 1.1 s |
| 8.3 MP progressive CMYK JPEG (2.3 MB) | 222 MiB | 1.2 s |
| 25 MP baseline CMYK JPEG (6 MB) | 226 MiB | 2.8 s |
| 25 MP 16-bit RGBA PNG, not rotated (0.3 MB) | 208 MiB | 2.7 s |
| 12.5 MP interlaced 16-bit RGBA PNG, not rotated (0.1 MB) | 197 MiB | 1.5 s |
| 50 MP 8-bit RGBA PNG, square, rotated, padded to 24.9 MB (the worst case) | 254 MiB | 2.4 s |
| Any image over the limits | input size + about 5 MiB | under 1 ms |

Bytes after the end of the image still count toward the 25 MB input, so a hostile file can pad itself to the limit; the worst-case row is padded for that reason. The other kinds that fill the decode budget (progressive 4:4:4, progressive and baseline CMYK, 16-bit and interlaced PNG) peaked at 232 to 244 MiB when padded to 24.9 MB.

Most of the time is the single-threaded Catmull-Rom scaling. The e2-micro's shared core is slower than the laptop, so expect several times longer there.

`DisplayJPEG` returns its memory to the operating system before it returns (`debug.FreeOSMemory`, 10 to 50 ms). Without that, the garbage collector let back-to-back calls pile up: three worst-case images in a row reached 660 MiB RSS without it, and eight stayed at 255 MiB with it.

**Integration requirement:** run at most one `DisplayJPEG` at a time, for example behind a semaphore of size 1 shared by every request that processes uploads. One call can hold about 265 MiB, so two at once do not fit next to Caddy, nginx and Postgres.

### Metadata rules

- Every field is `missing` (absent, blank or zeroed), `malformed` (present but unusable) or `usable`. `ExtractMetadata` sets a value field only for `usable`, so map the other two states to `null`.
- GPS: both coordinates are range-checked (latitude within 90, longitude within 180) and set together or not at all. A zero denominator, a bad hemisphere letter or an out-of-range value is `malformed`; a file with no GPS tags is `missing`.
- `CaptureTimeLocal` is the camera's wall-clock time in the form `2025-07-14T09:30:00`, matching `capture_time_local`. It carries no zone and is never treated as UTC.
- `CaptureUTCOffset` (`+hh:mm`) is only what the file states, from `OffsetTimeOriginal`. When it is absent it stays `missing`; no offset is guessed from the GPS position or the server's time zone. Time and offset are judged independently.
- Malformed EXIF is never fatal. A damaged EXIF block only marks the fields `malformed` and adds a line to `Diagnostics`, and `DisplayJPEG` still produces a display image, just unrotated.
- `Diagnostics` holds one human-readable line per missing or malformed field, for logs.

### How the worker should use it

```go
data, err := io.ReadAll(io.LimitReader(obj, photoproc.MaxInputBytes+1))
// A read error is transient: retry or return 500, never persist it as failed.

display, err := photoproc.DisplayJPEG(data)
var perr *photoproc.Error
if errors.As(err, &perr) {
    // Persist state=failed with error_code=perr.Code.
} else if err != nil {
    // Internal failure: return 500.
}

// Cannot fail once DisplayJPEG succeeded: both accept a JPEG by its FF D8
// signature and a PNG by its 8-byte signature.
meta, _ := photoproc.ExtractMetadata(data)
```

Then:

1. Upload `display` as a new object next to the original; never overwrite the original.
2. State is `ready` when `meta.GPSStatus == photoproc.Usable`, otherwise `needs-location`. An image with no GPS or no capture time still gets a display image.
3. `exif_lat` and `exif_lng` come from `meta.GPS` (both `null` when it is nil).
4. `capture_time_local` and `capture_utc_offset` are `null` unless their status is `usable`.
5. Log `meta.Diagnostics`.

Not done here and left for the integration work: running one `DisplayJPEG` at a time (required, see [Resource use](#resource-use)), adding an `error_code` enum (`unsupported_type`, `file_too_large`, `image_too_large`, `invalid_image`) to the OpenAPI contract after team review, and checking the GCS object size before downloading it (#21).

### Limitations

- No HEIC, WebP or GIF input, and no animation.
- The ICC profile is dropped, so Display-P3 phone photos appear as sRGB and slightly less saturated.
- Only `DateTimeOriginal` and `OffsetTimeOriginal` are read; there is no fallback to `OffsetTime` or the GPS timestamp.
- One display size only; there is no thumbnail.
- The whole file and the decoded image are held in memory, so progressive, RGB and CMYK JPEGs and 16-bit or interlaced PNGs have lower size limits than baseline YCbCr JPEGs (see [Resource use](#resource-use)).
- An orientation value outside 1-8 displays the image as stored.
- `Kernel.Transform` takes about four times as long as `CatmullRom.Scale` for the scaling step, and 2 to 3 times as long end to end (0.95 s instead of 0.35 s for a 12 MP JPEG), in exchange for having no scaler buffer. If CPU time on the VM becomes the bottleneck, the upgrade is a row-streaming separable Catmull-Rom: measured at 0.25 s for 12 MP with the same pixels to within rounding, but about 70 lines of resampling code. Bilinear and box filters were measured too and alias visibly on fine patterns.

## Docker

Build the container image:

```bash
docker build -t mapmory-backend .
```

The server now initializes Firebase at startup and requires `FIREBASE_PROJECT_ID`. **Building the image does not provide runtime credentials.** To run it, configure an appropriate Google Cloud runtime identity (for example, an attached service account on Google Cloud) and set `FIREBASE_PROJECT_ID=mapmory-development` in the deployment environment. Simply running `docker run -p 8080:8080 mapmory-backend` without this configuration is no longer a supported smoke test.

For local HTTP smoke tests, use `go run ./cmd/server` with the keyless ADC setup above. Local Docker credential forwarding is not documented here; do not bake ADC files or private keys into images.

The image uses a distroless runtime and runs as a nonroot user. It has no shell or package manager. Container restarts also discard the current in-memory trips.

## Planned architecture and next steps

The agreed target architecture uses a Go REST API, PostgreSQL for metadata, Google Cloud Storage with signed upload URLs for photos, Firebase Auth for identity, and Google Cloud deployment. These are **design decisions, not claims of current implementation**.

Next integration milestones include database user resolution and trip ownership checks, database migrations and persistence, photo upload registration/completion, asynchronous metadata processing, and retrieval of photo status. See the [OpenAPI contract](openapi/openapi.yaml) for the proposed request and response shapes; it remains a review draft and may evolve with frontend integration.
