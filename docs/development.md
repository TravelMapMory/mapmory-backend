# Development reference

For a quick start, see the [README](../README.md). This document records the current prototype API, repository layout, and test commands.

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
| `internal/place/place.go` | Provider-independent place resolver, result type and errors |
| `internal/place/geoapify/geoapify.go` | Geoapify reverse-geocoding adapter |
| `docs/place-resolution.md` | Configuration, attribution and provider-extension notes |
| `openapi/openapi.yaml` | Proposed API specification |
| `requests.http` | Manual API requests |

The HTTP handlers use Go's standard `net/http` router and structured JSON errors. The in-memory trip store is synchronized, but does not persist or enforce ownership.

### Database migrations

Database schema changes use PostgreSQL 17 and Goose v3.29.0. See [Database migrations](database-migrations.md) for local setup, migration commands, and testing.

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

