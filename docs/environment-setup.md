# Environment and local setup

This guide covers the backend's legacy prototype mode, persistent local mode, and Firebase Authentication emulator. It supplements the [README](../README.md), [Application-user resolution](application-user-resolution.md), and [Database migrations](database-migrations.md). The React frontend has its own [environment setup guide](https://github.com/TravelMapMory/mapmory-frontend/blob/main/docs/environment-setup.md); Vite configuration belongs there, not in this repository.

## Prerequisites

- Go **1.26.1**, as declared in [`go.mod`](../go.mod).
- For persistent mode: Docker, PostgreSQL 17 (the documented container uses Docker), and Goose v3.29.0.
- Firebase Admin SDK initialization for every server mode: either the team's keyless Google Application Default Credentials (ADC) or a matching Firebase Auth emulator. See [Firebase authentication setup](firebase-auth-setup.md).
- For persistent GCS mode only: a private Google Cloud Storage bucket and appropriate signing/storage permissions.

From the backend repository root:

```bash
cp .env.example .env
chmod 600 .env
```

The Go server loads eight allowlisted server settings from `.env` on startup. **Exported environment variables override the file, including empty values.** The server does not read Firebase test-password fields from `.env`. Never commit `.env`.

## Configuration matrix

| Setting | Legacy prototype | Persistent local (recommended) | Persistent GCS |
| --- | --- | --- | --- |
| `FIREBASE_PROJECT_ID` | Required | Required | Required |
| `MAPMORY_ENABLE_DB_USER_RESOLUTION` | `false` | `true` | `true` |
| `DATABASE_URL` | Not used | Migrated local PostgreSQL | Migrated PostgreSQL |
| `MAPMORY_PHOTO_STORAGE` | Not needed | `local` | `gcs` |
| `MAPMORY_LOCAL_PHOTO_DIR` | Not used | `.local-photos` | Not used |
| `MAPMORY_PHOTO_BUCKET` | Empty | **Empty** | Private GCS bucket name |
| `MAPMORY_UPLOAD_SIGNER` | Not used | Empty | Signing identity if required |
| `GEOAPIFY_API_KEY` | Optional | Optional | Optional |

**Two independent choices:** `MAPMORY_ENABLE_DB_USER_RESOLUTION` enables persistent, owner-scoped database routes; `MAPMORY_PHOTO_STORAGE` selects filesystem or GCS image storage once database mode is enabled. Persistent trips alone do not enable photo/upload/share endpoints without a configured photo store. Unlike the frontend's mock mode, the backend's legacy prototype is public and in-memory—**never use it for private content**.

`PORT` (default `8080`) and `FIREBASE_AUTH_EMULATOR_HOST` are **process environment variables**, not values read from `.env` by the server's loader. Google ADC is separate from the Firebase Web API key. `GOOGLE_APPLICATION_CREDENTIALS` is a Google SDK credential-file override, not a required MapMory setting.

`FIREBASE_WEB_API_KEY`, `TEST_EMAIL`, `TEST_PASSWORD`, and `FIREBASE_ID_TOKEN` in `.env.example` are used by [`scripts/firebase-id-token.sh`](../scripts/firebase-id-token.sh) and manual testing, **not by the Go API**. Keep passwords and tokens out of logs, commits, and frontend `VITE_*` settings.

## 1. Legacy prototype mode

To run the old public in-memory trip prototype, set:

```dotenv
FIREBASE_PROJECT_ID=mapmory-development
MAPMORY_ENABLE_DB_USER_RESOLUTION=false
```

Configure Firebase Admin SDK credentials as described in [Firebase authentication setup](firebase-auth-setup.md), then start:

```bash
go run ./cmd/server
curl -i http://127.0.0.1:8080/healthz
```

`GET /healthz` is public and `/api/auth/me` verifies Firebase ID tokens. Legacy trip routes do not provide persistent ownership isolation, and `/api/users/me` is unavailable. Restarting the process loses prototype trips. Use **persistent local mode** for actual end-to-end application development.

## 2. Persistent local mode (recommended)

This runs the real authenticated upload → process → correct → share workflow using PostgreSQL and a **loopback-only local photo store**. It does not need a GCS bucket, but still requires Firebase authentication.

**Start local PostgreSQL 17** (first run):

```bash
docker run -d \
  --name mapmory-postgres \
  -e POSTGRES_USER=mapmory \
  -e POSTGRES_PASSWORD=mapmory-local \
  -e POSTGRES_DB=mapmory \
  -p 127.0.0.1:5432:5432 \
  -v mapmory-postgres-data:/var/lib/postgresql/data \
  postgres:17

docker exec mapmory-postgres pg_isready -U mapmory -d mapmory
```

On later runs, use `docker start mapmory-postgres`. The example password and `sslmode=disable` are acceptable **only for this localhost-only development container**. If port 5432 is occupied, change the host port and `DATABASE_URL` together.

**Configure `.env`:**

```dotenv
FIREBASE_PROJECT_ID=mapmory-development
MAPMORY_ENABLE_DB_USER_RESOLUTION=true
DATABASE_URL=postgres://mapmory:mapmory-local@127.0.0.1:5432/mapmory?sslmode=disable
MAPMORY_PHOTO_STORAGE=local
MAPMORY_LOCAL_PHOTO_DIR=.local-photos
MAPMORY_PHOTO_BUCKET=
MAPMORY_UPLOAD_SIGNER=
GEOAPIFY_API_KEY=
```

The Geoapify key is optional. Without it, uploads and geographic browsing with existing coordinates still work, but automatic place labels and world-place search are unavailable.

**Install Goose and apply all migrations:**

```bash
go install github.com/pressly/goose/v3/cmd/goose@v3.29.0
export DATABASE_URL="$(sed -n 's/^DATABASE_URL=//p' .env | tail -n 1)"
goose -dir db/migrations postgres "$DATABASE_URL" up
goose -dir db/migrations postgres "$DATABASE_URL" status
```

Goose **does not automatically load `.env`**, and the Go server **does not run migrations at startup**. Apply all current migrations in [`db/migrations/`](../db/migrations/) (through `00009`). Avoid exporting a database URL from an untrusted `.env`; the shown command is for your own local file.

**Configure Firebase:** follow [Firebase authentication setup](firebase-auth-setup.md) for the shared `mapmory-development` project and keyless ADC, or use the emulator configuration in Section 3 below. `.env.example` does not provide usable Google credentials.

**Run the API:**

```bash
go run ./cmd/server
curl -i http://127.0.0.1:8080/healthz
```

The local photo adapter binds only to loopback and stores original files and display JPEGs under `.local-photos/`. It issues expiring signed PUT/GET URLs for local development; it is **not a GCS emulator or an appropriate public deployment target**. See [Local photo storage](local-photo-storage.md) for its security and deletion limitations.

Configure the React frontend separately, using the [frontend environment guide](https://github.com/TravelMapMory/mapmory-frontend/blob/main/docs/environment-setup.md): set `VITE_API_URL=http://127.0.0.1:8080` and Firebase Web settings for the **same Firebase project**. A live frontend does not fall back to mock data if the API fails.

For **persistent GCS mode**, use the same Firebase/PostgreSQL/migration steps but set `MAPMORY_PHOTO_STORAGE=gcs`, configure `MAPMORY_PHOTO_BUCKET` for a private bucket, and provide appropriate IAM/signing permissions. See [Deployment](../deploy/README.md) and [Local photo storage](local-photo-storage.md). Do not use the local signing or bucket configuration in production.

## 3. Firebase Auth emulator

Start the Auth emulator from a separate shell (Firebase CLI is required):

```bash
npx firebase-tools emulators:start --only auth --project demo-mapmory
```

**For backend integration with the emulator**, keep PostgreSQL and photo storage from Section 2, change the Firebase project in `.env` to match the emulator, and export the emulator host in the shell that launches Go:

```dotenv
# Backend .env
FIREBASE_PROJECT_ID=demo-mapmory
MAPMORY_ENABLE_DB_USER_RESOLUTION=true
```

```bash
export FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9099
go run ./cmd/server
```

Use the same project ID and emulator URL in the frontend's `.env.local` (see its [Auth emulator instructions](https://github.com/TravelMapMory/mapmory-frontend/blob/main/docs/environment-setup.md)). Emulator-issued tokens are **only for local testing**. Unset `FIREBASE_AUTH_EMULATOR_HOST` before connecting to real Firebase; never enable it in the deployed backend. Check SDK initialization requirements for your environment rather than mixing real Firebase credentials or tokens with the emulator.

To obtain an ID token for manual backend tests, see [Firebase authentication testing](firebase-auth-testing.md). The helper respects `FIREBASE_AUTH_EMULATOR_HOST` and uses disposable test accounts.

## Testing

Unit and fake-backed HTTP tests require neither a running PostgreSQL server nor real Firebase credentials:

```bash
go test ./...
go vet ./...
go build ./...
```

For isolated PostgreSQL integration and migration testing, with Docker and Goose available:

```bash
./scripts/test-postgres-integration.sh
./scripts/test-migrations.sh
```

Both scripts create and dispose of their own PostgreSQL containers; they do **not** operate on your normal `DATABASE_URL`. See [Database migrations](database-migrations.md) for schema-specific testing.

For a manual persistent smoke test, sign in through the configured frontend, create a trip, upload a JPEG or PNG with GPS metadata, upload one without GPS, correct its location, refresh to verify persistence, create a link share, open it signed out, and revoke it. New public shares require a Firebase-verified email. [Frontend live smoke test](https://github.com/TravelMapMory/mapmory-frontend/blob/main/docs/live-backend-smoke-test.md) provides the fuller checklist.

To verify authentication from a terminal, see [Firebase authentication testing](firebase-auth-testing.md) and [`requests.http`](../requests.http). Do not mistake an unauthenticated `401` from `/api/auth/me` for a failed health check.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Server fails to start in persistent mode | Database container readiness, `DATABASE_URL`, migrations, and exported variables overriding `.env`. There is no memory fallback. |
| `GET /api/users/me` returns `404` | Database user resolution is disabled; set `MAPMORY_ENABLE_DB_USER_RESOLUTION=true` and restart. |
| Authenticated requests return `401` | Firebase project mismatch, expired ID token, Admin SDK credentials, or mismatched emulator configuration. |
| Upload or sharing routes are absent | Persistent mode also requires a configured photo store and current migrations. |
| Local photo mode rejects startup | `MAPMORY_PHOTO_BUCKET` must be empty; clear any exported bucket value too. |
| Upload/display-image URLs fail | Confirm loopback host, local storage directory, content type, signing URL expiry, and Vite origin. |
| Geoapify labels or place search fail | Check backend `GEOAPIFY_API_KEY`; the frontend `VITE_GEOAPIFY_KEY` is for map tiles, not reverse geocoding. |
| Goose cannot connect | It does not load `.env`; confirm the explicitly supplied URL points to the intended development database. |
| Old signed local URLs fail after restart | Expected: local mode uses an ephemeral signing key; request fresh display URLs. |
| Migration/integration tests cannot run | Install Docker and Goose; confirm the Docker daemon is running. |

For ownership and Firebase identity resolution, see [Application-user resolution](application-user-resolution.md). For GCS configuration and production deployment, see [Deployment](../deploy/README.md).
