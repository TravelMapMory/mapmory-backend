# Development reference

See the [README](../README.md) for startup and [Application-user resolution](application-user-resolution.md) for the database-backed authentication flow.

## HTTP endpoints

| Method | Route | Behavior |
| --- | --- | --- |
| `GET` | `/healthz` | Public; `{"status":"ok"}` |
| `GET` | `/api/auth/me` | Firebase ID token required; `{"uid":"..."}` (`401` otherwise) |
| `GET` | `/api/users/me` | Opt-in PostgreSQL resolution; returns `id` and `auth_subject` (`401` invalid token; `503` resolution failure) |
| `GET` | `/api/trips` | Public prototype; `{ "items": [...], "next_cursor": null }` |
| `POST` | `/api/trips` | Public prototype; creates a trip, returns `201` |
| `GET` | `/api/trips/{trip_id}` | Public prototype; returns trip or JSON `404` |

Trip titles are trimmed, required, and limited to 120 Unicode characters. Invalid creation requests return `400` with `error.code: "invalid_request"`. All prototype trips are stored in process memory and shared across callers, with no authentication or ownership protection. A server restart discards them.

Manual HTTP examples are in [requests.http](../requests.http). The [OpenAPI draft](../openapi/openapi.yaml) describes future owner-scoped operations, not necessarily current behavior.

## Code map

| Directory | Responsibility |
| --- | --- |
| `cmd/server/` | HTTP routes, prototype trip storage, and server startup |
| `internal/auth/` | Firebase Admin SDK verifier and bearer-token middleware |
| `internal/appuser/` | Trusted MapMory UUID context and user-resolution middleware |
| `internal/users/` | PostgreSQL find-or-create repository for Firebase UIDs |
| `internal/postgres/` | PostgreSQL connection pool |
| `db/migrations/` | Goose SQL migrations |
| `internal/photometa/`, `cmd/photometa/` | Local JPEG/PNG metadata inspection |
| `internal/photoproc/` | Metadata normalization and display-image generation |
| `internal/place/` | Provider-independent location resolution; Geoapify adapter |
| `openapi/` | Proposed API contract |

More details: [Database migrations](database-migrations.md), [Photo metadata](photo-metadata.md), [Photo processing](photo-processing.md), and [Place resolution](place-resolution.md).

## Tests

```bash
go test ./...
go vet ./...
go build ./...
gofmt -l .   # no output expected
```

With Docker and Goose installed:

```bash
./scripts/test-migrations.sh            # migration lifecycle and constraints
./scripts/test-postgres-integration.sh  # user repository, pool and race tests
```

The default Go suite uses fakes for authentication and user resolution; it does not need Firebase credentials or a live database. Neither the default suite nor the isolated integration tests establish production readiness.
