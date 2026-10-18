# MapMory (Backend)

See [Backend coding conventions](docs/coding-conventions.md) for repository expectations covering formatting, naming, error handling, HTTP handlers, and testing.

MapMory is a photo-first travel-memory application. Its planned backend manages trips and photos, processes image metadata (including EXIF coordinates and capture times), and supports location-based browsing and sharing.

This repository contains the **Go REST API**. The current implementation is an early prototype: it provides a health check and basic trip creation and retrieval with **in-memory storage**. Photo processing and persistent storage are not implemented yet. Firebase ID-token verification is implemented for a diagnostic endpoint, but trip authorization is not.

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
