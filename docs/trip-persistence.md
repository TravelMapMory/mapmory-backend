# Authenticated trip persistence

Trip persistence is implemented when the backend starts with **`MAPMORY_ENABLE_DB_USER_RESOLUTION=true`** and a reachable migrated PostgreSQL database. In that mode the three trip routes are authenticated, owner-scoped, and persistent. When the flag is **false**, the legacy trip prototype is public, shared in process memory, and discarded on restart. Never use the legacy mode to hold private travel information.

## Request and ownership model

```text
Firebase ID token → auth.RequireIdentity → verified Firebase UID
 → appuser.RequireIdentity → users.id (trusted application UUID)
 → persistent trip handlers → trips.Repository → PostgreSQL trips.owner_id
```

The server obtains `owner_id` exclusively from verified request context. The client cannot specify or change an owner; unknown JSON request fields are rejected. The trip repository scopes `ListByOwner` and `GetByID` queries by `owner_id`. Both nonexistent and foreign trips return the same JSON `404`. `GET /healthz` is public; `GET /api/auth/me` verifies Firebase but does not need PostgreSQL; `/api/users/me` and persistent trip routes require user resolution.

## Supported endpoints (database mode)

| Request | Behavior |
| --- | --- |
| `GET /api/trips` | `200` `{ "items": [...], "next_cursor": null }`; newest first; currently **no pagination** |
| `POST /api/trips` | `201` with new trip; JSON object with required `title` and optional `notes` |
| `GET /api/trips/{trip_id}` | `200` for an owned trip; `404` for unknown, malformed, or another user's ID |

Trip responses contain `id`, `title`, `notes`, `created_at`, `photo_count: 0` and `cover: null`. The owner UUID is not returned. The HTTP layer trims titles and rejects empty titles or those exceeding 120 Unicode code points. An unknown field, extra JSON value, or invalid body returns JSON `400` (`invalid_request`). Missing/invalid Firebase credentials return JSON `401` (`unauthorized`); application-user resolution failure returns `503` (`user_resolution_unavailable`); unexpected trip-storage failures return JSON `500` (`internal_error`) without database internals. Unsupported paths/methods may return the default plain-text `404`/`405` from Go's router. `HEAD` is matched by `GET` route patterns in Go's ServeMux.

Creation is **not idempotent**: repeating `POST` may create another trip. Do not retry a write automatically after an uncertain network failure. Trip cover/photo counts are placeholders until the photo API is integrated. The OpenAPI draft also describes proposed trip cursor pagination and future photo/upload operations not implemented here.

## Local development

1. Follow [Firebase backend setup](firebase-auth-setup.md) and [Application-user resolution](application-user-resolution.md) to configure credentials and a persistent local PostgreSQL 17 container.
2. Set `MAPMORY_ENABLE_DB_USER_RESOLUTION=true` and the correct `DATABASE_URL` in your ignored `.env`. Explicit shell exports override `.env`.
3. Install Goose v3.29.0 and apply **both** migrations: `goose -dir db/migrations postgres "$DATABASE_URL" up` (the Goose CLI does not load `.env` automatically).
4. Run `go run ./cmd/server`. A missing or unreachable database prevents startup in database mode; there is no silent memory fallback.
5. Refresh a disposable account's token with `scripts/firebase-id-token.sh` and exercise authenticated requests in [requests.http](../requests.http).
6. Create a trip, restart the server, and retrieve the same ID. With another test account, verify that the first user's trip is not listed and returns `404` on detail lookup.

Note: running `./scripts/test-postgres-integration.sh` creates a disposable test database which is removed after the run. It is not the local persistent development database.

## Architecture and tests

`cmd/server/database_dependencies.go` constructs `users.Repository` and `trips.PostgresRepository` using **one** `*sql.DB` pool. Startup owns and closes that pool. `internal/trips/repository.go` defines context-aware owner-scoped operations; `internal/trips/postgres.go` implements SQL queries, and HTTP handlers map stored trips to the existing response shape. Database mode explicitly registers protected routes and does not include the legacy catch-all router.

```bash
go test -count=1 './...'
go test -race -count=1 './...'
go vet './...'
go build './...'
./scripts/test-migrations.sh
./scripts/test-postgres-integration.sh
```

The default Go suite uses fakes and needs no database. The PostgreSQL integration harness provisions isolated PostgreSQL 17, applies migrations and runs repository and HTTP integration tests with the race detector. HTTP integration tests use synthetic verified Firebase identities but real user resolution, trip repositories and SQL. They cover cross-user isolation and persistence through a fresh database pool; they do not test the Firebase Admin SDK or production deployment.

## Deployment limitations

Database mode is not automatically enabled in the deployed VM. Provisioning PostgreSQL, managing credentials and applying migrations in the target environment remain separate deployment work (backend #42). Do not set the feature flag true until that setup is ready and validated. Never use the sample local password or `sslmode=disable` across an untrusted network. Do not log Firebase tokens or connection-string credentials.
