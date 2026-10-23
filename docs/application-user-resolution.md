# Application-user resolution

Firebase Authentication verifies the caller; PostgreSQL identifies that caller within MapMory. `GET /api/users/me` demonstrates the full path without enabling authorization on the prototype trip routes.

## Request flow

```text
Authorization: Bearer <Firebase ID token>
  -> auth.RequireIdentity        verifies token; records Firebase UID
  -> appuser.RequireIdentity     finds/creates users row by auth_subject
  -> /api/users/me               returns PostgreSQL id + verified UID
```

The repository uses `INSERT ... ON CONFLICT (auth_subject) DO NOTHING`, followed by a separate `SELECT` for an existing user. PostgreSQL's unique constraint ensures concurrent first-time requests cannot create duplicate users. Handlers read the internal UUID from the trusted request context, never from client-supplied `owner_id` fields.

`/api/auth/me` verifies Firebase only and works without PostgreSQL. `/api/users/me` additionally requires database-backed user resolution.

## Local setup

Prerequisites: Go (see `go.mod`), Docker, Goose v3.29.0 (installation in [Database migrations](database-migrations.md)), and [Firebase backend credentials](firebase-auth-setup.md). Run commands from the backend repository root.

1. Create your ignored local configuration if it doesn't exist:

   ```bash
   cp -n .env.example .env
   chmod 600 .env
   ```

2. Start a persistent, localhost-only PostgreSQL 17 container:

   ```bash
   docker run -d \
     --name mapmory-postgres \
     -e POSTGRES_USER=mapmory \
     -e POSTGRES_PASSWORD=mapmory-local \
     -e POSTGRES_DB=mapmory \
     -p 127.0.0.1:5432:5432 \
     -v mapmory-postgres-data:/var/lib/postgresql/data \
     postgres:17
   ```

   On later days, run `docker start mapmory-postgres`. Verify readiness with `docker exec mapmory-postgres pg_isready -U mapmory -d mapmory`. Use a different host port if 5432 is occupied. The local password and disabled TLS are **not** suitable for shared or production databases.

3. Set these entries in `.env`, leaving other settings intact:

   ```dotenv
   FIREBASE_PROJECT_ID=mapmory-development
   MAPMORY_ENABLE_DB_USER_RESOLUTION=true
   DATABASE_URL=postgres://mapmory:mapmory-local@127.0.0.1:5432/mapmory?sslmode=disable
   ```

4. Apply the migration (the Goose CLI does **not** read `.env` automatically):

   ```bash
   export DATABASE_URL="$(sed -n 's/^DATABASE_URL=//p' .env | tail -n 1)"
   goose -dir db/migrations postgres "$DATABASE_URL" up
   goose -dir db/migrations postgres "$DATABASE_URL" status
   ```

5. Launch the backend:

   ```bash
   go run ./cmd/server
   ```

   The backend reads the three server keys from `.env` on startup. **Exported variables take precedence over `.env`**, even if the exported value is empty. Use `unset MAPMORY_ENABLE_DB_USER_RESOLUTION DATABASE_URL` if earlier shell settings are overriding the file. The backend never applies migrations automatically.

## Verify with Firebase

Follow [Firebase authentication testing](firebase-auth-testing.md) to create a disposable account and refresh its ID token:

```bash
scripts/firebase-id-token.sh
```

Use [requests.http](../requests.http) in VS Code REST Client to send the authenticated `/api/auth/me` and `/api/users/me` requests. The helper writes `FIREBASE_ID_TOKEN` to the ignored `.env`; it does **not** export the token into your terminal. Alternatively, explicitly obtain the token in the shell:

```bash
export FIREBASE_ID_TOKEN="$(scripts/firebase-id-token.sh --print)"
curl -i -H "Authorization: Bearer $FIREBASE_ID_TOKEN" http://localhost:8080/api/users/me
```

An example successful response is:

```json
{"id":"c5cfd147-12e0-47a6-a2a2-f4f93fb65063","auth_subject":"firebase-user-123"}
```

Repeat the request: the internal `id` should remain unchanged. To inspect the actual record:

```bash
docker exec -it mapmory-postgres psql -U mapmory -d mapmory \
  -c 'SELECT id, auth_subject, display_name, created_at FROM users;'
```

## Failures and testing

| Behavior | Meaning |
| --- | --- |
| `/api/auth/me` returns `401` | Missing or invalid Firebase token |
| `/api/users/me` returns `404` | Database-backed route not enabled at startup |
| `/api/users/me` returns `401` | Missing or invalid Firebase token |
| `/api/users/me` returns `503` | User lookup failed; no identity granted |
| Server fails on startup | Database feature is enabled, but `DATABASE_URL` is missing or unreachable |

For `404`, check the flag in `.env`, shell overrides, and restart the Go process. For `503`, check PostgreSQL connectivity and whether Goose migrations were applied to the database in the server's URL. Internal database errors are not included in HTTP responses.

The repository integration suite uses its **own disposable** PostgreSQL container and never the developer's `DATABASE_URL`:

```bash
./scripts/test-postgres-integration.sh
```

It covers repeated and concurrent provisioning, cancellation, and database failures. The middleware and HTTP router have separate tests using fake Firebase verifiers and user resolvers (`go test ./...`).

**Current limitations:** Production VM configuration for PostgreSQL is pending. Trip endpoints remain public and in-memory; [trip ownership enforcement](https://github.com/TravelMapMory/mapmory-backend/issues/9) is separate. Ordinary Firebase ID-token verification does not check revocation by default.
