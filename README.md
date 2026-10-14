# MapMory (Backend)

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
go vet ./...
go build ./...
```

CI runs vet, build, tests, and a Docker image build. These checks do not establish production readiness or test Firebase, PostgreSQL, or Google Cloud Storage integration.

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
