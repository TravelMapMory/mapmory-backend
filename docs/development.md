# Development reference

See the [README](../README.md) for startup and [Application-user resolution](application-user-resolution.md) for the database-backed authentication flow.

## HTTP endpoints

| Method | Route | Behavior |
| --- | --- | --- |
| `GET` | `/healthz` | Public; `{"status":"ok"}` |
| `GET` | `/api/auth/me` | Firebase ID token required; `{"uid":"..."}` (`401` otherwise) |
| `GET` | `/api/users/me` | Opt-in PostgreSQL resolution; returns `id` and `auth_subject` (`401` invalid token; `503` resolution failure) |
| `GET` | `/api/trips` | Database mode: authenticated, owner-scoped list; `{ "items": [...], "next_cursor": null }` |
| `POST` | `/api/trips` | Database mode: authenticated create owned by resolved user; returns `201` |
| `GET` | `/api/trips/{trip_id}` | Database mode: authenticated owner-only detail; returns trip or JSON `404` |
| `PATCH` | `/api/trips/{trip_id}` | Database mode: authenticated owner update of title/notes |
| `POST` | `/api/trips/{trip_id}/batches` | Upload mode: register JPEG/PNG files and obtain signed PUT URLs |
| `POST` | `/api/batches/{batch_id}/files/{client_file_id}/complete` | Upload mode: validate uploaded object, process and persist photo |
| `GET` | `/api/batches/{batch_id}` | Upload mode: owner-only batch status |
| `GET` | `/api/trips/{trip_id}/photos` | Upload mode: owner-only photo list in capture order |
| `GET` | `/api/photos/{photo_id}` | Upload mode: owner-only photo detail and signed display image |
| `PATCH` | `/api/photos/{photo_id}/location` | Upload mode: owner location correction or label confirmation |
| `GET` | `/api/map/photos` | Upload mode: owner-scoped, filtered photo markers |
| `GET` | `/api/me/summary` | Upload mode: Dashboard counts and recent uploads |
| `GET` | `/api/places/search?q=...` | Database mode: public, rate-limited provider place search; 503 without provider |
| `GET`, `POST` | `/api/trips/{trip_id}/shares` | Upload mode: owner list/create fixed photo shares |
| `DELETE` | `/api/shares/{share_id}` | Upload mode: owner revocation; idempotent |
| `GET` | `/api/s/{token}` | Upload mode: unauthenticated link recipient view; 410 if revoked |
| `GET` | `/api/public/shares/{share_id}` | Upload mode: unauthenticated active public share |
| `GET` | `/api/public/map?bbox=...&zoom=...` | Upload mode: rate-limited clustered public map; at most 200 clusters |

Trip titles are trimmed, required, and limited to 120 Unicode characters. Invalid creation requests return `400` with `error.code: "invalid_request"`. When `MAPMORY_ENABLE_DB_USER_RESOLUTION=true`, trips are persisted to PostgreSQL and all supported trip routes require a Firebase token and resolved user UUID. Unknown and foreign trip IDs both return `404`; storage errors return generic `500` responses. The list is currently unpaginated and `next_cursor` is always null. When disabled, the legacy prototype still stores trips in memory shared across callers, without trip authentication or ownership protection; restarting discards them. **Do not use legacy mode for private data.**

"Upload mode" means database mode with `MAPMORY_PHOTO_BUCKET` set. The server does not register upload, photo, overview or sharing routes without the bucket. PostgreSQL schema migrations through 00007 are required for sharing. Public recipient responses disclose only selected photos, signed display images and coarse location coordinates; existing signed URLs may outlive revocation until expiration.

Manual HTTP examples are in [requests.http](../requests.http). See [Trip persistence](trip-persistence.md) and the [OpenAPI contract](../openapi/openapi.yaml) for implemented operations and remaining proposed trip pagination, deletion and retry behavior.

## Code map

| Directory | Responsibility |
| --- | --- |
| `cmd/server/` | HTTP routes, authenticated persistence and legacy prototype, server startup |
| `internal/auth/` | Firebase Admin SDK verifier and bearer-token middleware |
| `internal/appuser/` | Trusted MapMory UUID context and user-resolution middleware |
| `internal/users/` | PostgreSQL find-or-create repository for Firebase UIDs |
| `internal/postgres/` | PostgreSQL connection pool |
| `internal/trips/` | Trip domain model, owner-scoped memory and PostgreSQL repositories |
| `internal/uploads/`, `internal/photos/`, `internal/photostorage/` | Upload lifecycle, processed photo persistence and signed GCS objects |
| `internal/shares/` | Owner-scoped sharing repository, coarse coordinates and public map clustering |
| `db/migrations/` | Goose SQL migrations |
| `internal/photometa/`, `cmd/photometa/` | Local JPEG/PNG metadata inspection |
| `internal/photoproc/` | Metadata normalization and display-image generation |
| `internal/place/` | Provider-independent location resolution; Geoapify adapter |
| `openapi/` | Implemented API contract with remaining proposals explicitly marked |

More details: [Trip persistence](trip-persistence.md), [Database migrations](database-migrations.md), [Photo metadata](photo-metadata.md), [Photo processing](photo-processing.md), and [Place resolution](place-resolution.md).

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
./scripts/test-postgres-integration.sh  # user, trip, upload, photo, share and HTTP integration; race-enabled
```

The default Go suite uses fakes for authentication and user resolution; it does not need Firebase credentials or a live database. Neither the default suite nor the isolated integration tests establish production readiness.
