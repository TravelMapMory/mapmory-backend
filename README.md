# MapMory (Backend)

See [Backend coding conventions](docs/coding-conventions.md) for repository expectations covering formatting, naming, error handling, HTTP handlers, and testing.

MapMory is a photo-first travel-memory application. Its planned backend manages trips and photos, processes image metadata (including EXIF coordinates and capture times), and supports location-based browsing and sharing.

This repository contains the **Go REST API**. The current implementation is an early prototype: it provides a health check and basic trip creation and retrieval with **in-memory storage**. Photo processing, Firebase authentication, and persistent storage are not implemented yet.

- [Frontend repository](https://github.com/TravelMapMory/mapmory-frontend)
- [OpenAPI contract](openapi/openapi.yaml) — target API behavior; not every operation is implemented

## Project status

| Capability | Status |
| --- | --- |
| Health check | Implemented |
| Create a trip | Implemented; in-memory |
| List trips | Implemented; no pagination yet |
| Get a trip by ID | Implemented; in-memory |
| Firebase ID token verification and trip ownership | Pending |
| PostgreSQL persistence | Pending |
| Photo metadata inspection command | Implemented; local files only |
| Photo registration, uploads, and processing | Pending |
| Location resolution and sharing | Pending |

**Important:** The OpenAPI specification describes authenticated, owner-scoped operations. The prototype currently has **no authentication or authorization**; all trips in the server's process-wide memory are accessible to callers. Do not deploy it as a production service holding private user data.

## Getting started

### Prerequisites

- Go 1.26.1 or newer (as declared in `go.mod`)

### Run locally

From the repository root:

```bash
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

More examples, including invalid inputs and missing trips, are in [`requests.http`](requests.http).

## Implemented endpoints

| Method | Path | Success | Notes |
| --- | --- | --- | --- |
| `GET` | `/healthz` | `200` | Returns `{"status":"ok"}` |
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

The contract also specifies `401` for missing or invalid Firebase tokens and `404` for trips owned by another user; **those access-control behaviors are not implemented in this prototype**.

## Code structure

| Path | Responsibility |
| --- | --- |
| `cmd/server/main.go` | HTTP server, routes, request handlers |
| `cmd/server/api_types.go` | JSON request and response types |
| `cmd/server/trip_store.go` | Thread-safe in-memory trip storage |
| `cmd/server/*_test.go` | Handler, storage, and API model tests |
| `cmd/photometa/main.go` | Photo metadata inspection command |
| `internal/photometa/photometa.go` | JPEG and PNG EXIF reader used by the command |
| `internal/photometa/testdata/` | Synthetic test images and the generator that writes them |
| `openapi/openapi.yaml` | Proposed API specification |
| `requests.http` | Manual API requests |

The HTTP handlers use Go's standard `net/http` router and structured JSON errors. The in-memory trip store is synchronized, but does not persist or enforce ownership.

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

CI runs a gofmt check, vet, build, tests, and a Docker image build. These checks do not establish production readiness or test Firebase, PostgreSQL, or Google Cloud Storage integration.

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

## Docker

Build the container image:

```bash
docker build -t mapmory-backend .
```

Run it:

```bash
docker run --rm -p 8080:8080 mapmory-backend
```

Check its health endpoint:

```bash
curl -i http://localhost:8080/healthz
```

To change the listening port:

```bash
docker run --rm -e PORT=9090 -p 9090:9090 mapmory-backend
```

The image uses a distroless runtime and runs as a nonroot user. It has no shell or package manager. Container restarts also discard the current in-memory trips.

## Planned architecture and next steps

The agreed target architecture uses a Go REST API, PostgreSQL for metadata, Google Cloud Storage with signed upload URLs for photos, Firebase Auth for identity, and Google Cloud deployment. These are **design decisions, not claims of current implementation**.

Next integration milestones include Firebase token verification and ownership checks, database migrations and persistence, photo upload registration/completion, asynchronous metadata processing, and retrieval of photo status. See the [OpenAPI contract](openapi/openapi.yaml) for the proposed request and response shapes; it remains a review draft and may evolve with frontend integration.
