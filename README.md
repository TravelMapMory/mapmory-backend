# MapMory (Backend)

See [Backend coding conventions](docs/coding-conventions.md) for repository expectations covering formatting, naming, error handling, HTTP handlers, and testing.

MapMory is a photo-first travel-memory application. Its planned backend manages trips and photos, processes image metadata (including EXIF coordinates and capture times), and supports location-based browsing and sharing.

This repository contains the **Go REST API**. The current implementation is an early prototype: it provides a health check and basic trip creation and retrieval with **in-memory storage**. Photo processing and persistent storage are not implemented yet. Firebase ID-token verification is implemented for a diagnostic endpoint, but trip authorization is not. The photo processing code exists as a standalone module (see [Photo processing](docs/photo-processing.md)).

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
| Place resolution (Geoapify reverse geocoding) | Implemented as a library; not wired into the API |
| Location correction and sharing | Pending |

**Important:** The OpenAPI specification describes authenticated, owner-scoped operations. The prototype verifies Firebase ID tokens on **`/api/auth/me` only**. Trip routes do **not** enforce authentication or ownership; all trips in the server's process-wide memory remain accessible to callers. Do not deploy it as a production service holding private user data.

## Getting started

### Prerequisites

- Go 1.26.1 or newer (as declared in `go.mod`).
- Firebase development project access and keyless Google Cloud ADC setup: [Firebase auth setup](docs/firebase-auth-setup.md).

The shared Firebase project is `mapmory-development`. Firebase **user ID tokens** authorize `/api/auth/me`; Google Cloud ADC credentials serve a different purpose and are **not** user ID tokens. Follow the [setup guide](docs/firebase-auth-setup.md) for access and backend credentials.

### Run locally

From the repository root, after completing the [keyless ADC setup](docs/firebase-auth-setup.md):

```bash
export FIREBASE_PROJECT_ID=mapmory-development
unset GOOGLE_APPLICATION_CREDENTIALS
unset FIREBASE_AUTH_EMULATOR_HOST
go run ./cmd/server
```

The server listens on `http://localhost:8080` (`PORT=9090 go run ./cmd/server` to override). Trips are stored only in memory and disappear on restart.

### Verify the API

```bash
curl -i http://localhost:8080/healthz       # 200
curl -i http://localhost:8080/api/auth/me   # 401 without a token
```

For authenticated testing, **create your own ignored `.env` explicitly**; the helper does not create it. Follow [Firebase auth testing](docs/firebase-auth-testing.md) for test credentials and `.env` permissions, then run:

```bash
scripts/firebase-id-token.sh
scripts/firebase-id-token.sh --check http://localhost:8080
```

The helper saves `FIREBASE_ID_TOKEN` for [requests.http](requests.http) (VS Code REST Client). It does not export the token into your shell. The server itself does not load `.env` automatically. Use only disposable test accounts and never commit credentials or tokens. The helper preserves existing `.env` permissions, so set `chmod 600 .env` yourself.

For trip-request examples, response shapes, and test commands, see [Development reference](docs/development.md) and [requests.http](requests.http).

## Development and component guides

- [Development reference](docs/development.md) — implemented endpoints, repository structure, tests, and checks.
- [Firebase authentication setup](docs/firebase-auth-setup.md) — development project, keyless ADC, and backend startup.
- [Firebase authentication testing](docs/firebase-auth-testing.md) — token helper, `requests.http`, and negative tests.
- [Database migrations](docs/database-migrations.md) — local PostgreSQL and Goose procedures.
- [Photo metadata inspection](docs/photo-metadata.md) — `photometa` command, fixtures, limits, and GCS comparisons.
- [Photo metadata experiments](docs/photo-metadata-experiments.md) — metadata findings and test fixtures.
- [Photo processing](docs/photo-processing.md) — image conversion, metadata rules, memory limits, and worker integration.
- [Place resolution](docs/place-resolution.md) — Geoapify integration and provider abstraction.
- [Coding conventions](docs/coding-conventions.md) — formatting, naming, errors, and tests.
- [Deployment](deploy/README.md) — deployment setup and operations.

## Docker

```bash
docker build -t mapmory-backend .
```

A build does not provide Firebase credentials. Runtime requires `FIREBASE_PROJECT_ID` and an appropriate Google Cloud identity; a bare `docker run` is not a supported smoke test. The image uses a nonroot distroless runtime, and restarts discard in-memory trips. See the [deployment guide](deploy/README.md) for details.

## Planned architecture and next steps

The agreed target architecture uses a Go REST API, PostgreSQL for metadata, Google Cloud Storage with signed upload URLs for photos, Firebase Auth for identity, and Google Cloud deployment. These are **design decisions, not claims of current implementation**.

Next integration milestones include database user resolution and trip ownership checks, database migrations and persistence, photo upload registration/completion, asynchronous metadata processing, and retrieval of photo status. See the [OpenAPI contract](openapi/openapi.yaml) for the proposed request and response shapes; it remains a review draft and may evolve with frontend integration.
