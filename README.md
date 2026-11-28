# MapMory backend

Go HTTP API for [MapMory](https://mapmory.app), a photo-first travel-memory application. The persistent API uses PostgreSQL, Firebase Authentication, photo storage and optional Geoapify place resolution. The React client lives in the [frontend repository](https://github.com/TravelMapMory/mapmory-frontend).

The server supports **persistent database mode** for authenticated owner workflows and a **legacy in-memory prototype** with public, non-persistent trip routes. Never use the prototype for private data.

## Quick start

**Requirements:** Go 1.26.1 (`go.mod`), Firebase Admin SDK initialization, and—in persistent mode—PostgreSQL 17 and Goose. Docker is used for the documented local database workflow.

For the recommended **persistent local** setup, follow [Environment and local setup](docs/environment-setup.md) from start to finish. It covers copying `.env.example`, starting PostgreSQL, applying Goose migrations, configuring Firebase (development credentials or the Auth emulator), and selecting local photo storage. Then start the API:

```bash
go run ./cmd/server
curl -i http://127.0.0.1:8080/healthz
```

The example `.env` targets localhost PostgreSQL and local photo storage, but **does not create PostgreSQL or configure Firebase credentials**. Migrations are not automatic, and Firebase Admin SDK initialization is required even in legacy prototype mode.

## Configuration

The server loads an allowlist of eight settings from the repository-root `.env`. Exported process variables take precedence, including empty values. Deployment may omit `.env` and supply configuration through its environment. See [Environment and local setup](docs/environment-setup.md) for requirements by mode and secret handling.

| Variable | Required for | Meaning |
| --- | --- | --- |
| `FIREBASE_PROJECT_ID` | All server modes | Project used to verify Firebase ID tokens; Admin credentials are configured separately |
| `MAPMORY_ENABLE_DB_USER_RESOLUTION` | Persistent mode | Set `true` for private owner-scoped PostgreSQL routes; `false` selects public prototype |
| `DATABASE_URL` | Persistent mode | **Secret:** PostgreSQL connection URL, with migrations applied |
| `MAPMORY_PHOTO_STORAGE` | Photo storage selection | `local` for development-only filesystem, `gcs` for Google Cloud Storage |
| `MAPMORY_LOCAL_PHOTO_DIR` | Local photo mode | Private directory for originals and processed images |
| `MAPMORY_PHOTO_BUCKET` | GCS photo mode | Private Google Cloud Storage bucket; empty in local mode |
| `MAPMORY_UPLOAD_SIGNER` | GCS, when needed | Explicit signing identity; often unset on the VM |
| `GEOAPIFY_API_KEY` | Optional place features | **Server secret:** place search and automatic labels; uploads work without it |

`PORT` (default `8080`) and `FIREBASE_AUTH_EMULATOR_HOST` are process-only variables, **not loaded from `.env`**. Google Application Default Credentials (ADC) are configured through the SDK / `gcloud` or runtime identity, not by setting the Firebase Web API key. `GOOGLE_APPLICATION_CREDENTIALS` is an optional Google SDK override, not a required MapMory variable.

Test-only `.env` keys (`FIREBASE_WEB_API_KEY`, `TEST_EMAIL`, `TEST_PASSWORD`, `FIREBASE_ID_TOKEN`) are consumed by the token helper, **not** the Go server. Never commit `.env`, passwords, tokens or service-account credentials.

### Development modes

| Mode | Configuration | Behavior |
| --- | --- | --- |
| Legacy prototype | Firebase initialization; database flag `false` | Public, in-memory trips; no persistent private workflow |
| Persistent local (recommended) | Firebase; database flag `true`; migrated PostgreSQL; `MAPMORY_PHOTO_STORAGE=local` | Private owner operations, signed local photo uploads and sharing; loopback-only |
| Persistent GCS | Firebase; migrated PostgreSQL; `MAPMORY_PHOTO_STORAGE=gcs`; private bucket and signing permissions | Real upload/processing/sharing against GCS |

Database mode **fails startup** if PostgreSQL is unavailable; it does not fall back to memory. Local photo mode replaces GCS for development, not Firebase or PostgreSQL. The frontend and backend must use the **same Firebase project** for authenticated live requests.

## Architecture and features

| Area | Behavior |
| --- | --- |
| Authentication | Firebase ID-token verification, user resolution to an internal application UUID and owner-scoped authorization |
| Trips and dashboard | Persistent trip CRUD, owner summary and map filters; legacy public prototype when database mode is disabled |
| Photos | Signed URL registration/upload/completion, EXIF extraction, EXIF-free display JPEG generation, gallery pagination and deletion |
| Places | Optional Geoapify resolution, search and manual location correction without rewriting EXIF/capture time |
| Sharing | Link-only and public collections, anonymous recipient views, revocation, reporting and moderation |
| Public map | Coarse shared-photo positions and bounded viewport clustering |

The API verifies ownership server-side; clients must not supply a trusted `owner_id`. A verified email is required to **create public** collections, but not to manage private trips or link-only shares. The public in-memory prototype does not provide equivalent security or persistence guarantees.

`GET /healthz` is public. `GET /api/auth/me` verifies the Firebase token, while `/api/users/me` and owner-specific routes require database mode. Photo and share APIs also require configured photo storage. See [OpenAPI](openapi/openapi.yaml) for endpoints and separately marked proposals, [Development reference](docs/development.md) for implementation details, and [Trip persistence](docs/trip-persistence.md) for ownership semantics.

## Development and testing

```bash
go test ./...
go vet ./...
go build ./...
./scripts/test-postgres-integration.sh  # Docker + Goose; isolated DB
./scripts/test-migrations.sh            # Docker + Goose; migration lifecycle
```

The ordinary Go tests do not require a live database or Firebase credentials; the server **does** require Firebase initialization. Integration scripts manage isolated temporary PostgreSQL instances rather than relying on your development `DATABASE_URL`. See [Firebase authentication testing](docs/firebase-auth-testing.md) and [Database migrations](docs/database-migrations.md).

For local API smoke tests, use [requests.http](requests.http) and the [Firebase ID-token helper](docs/firebase-auth-testing.md). `curl -i http://127.0.0.1:8080/api/auth/me` should return `401` without a token.

## Deployment

The production API runs on a Google Cloud VM behind Caddy, with PostgreSQL, private GCS photo storage and Firebase Authentication. Build the image using `docker build -t mapmory-backend .`. Runtime identity, secrets, database access and GCS signing permissions must be configured independently. See [Deployment](deploy/README.md) for VM and CI behavior.

Do not deploy the in-memory prototype or local filesystem photo mode publicly.

## Known limitations

- Geoapify place search is unavailable without `GEOAPIFY_API_KEY`; photo uploads still work, while unresolved labels remain pending.
- Revoking a share stops subsequent collection lookups, but previously issued signed display-image URLs can remain usable until they expire.
- The OpenAPI specification includes explicitly identified proposed endpoints that are not implemented.
- Production end-to-end upload, sharing, GCS signing and database configuration must be checked against the actual deployed environment; a successful local test does not establish production readiness.

## Documentation and credits

- [Environment and local setup](docs/environment-setup.md) — required/optional variables and the persistent local workflow
- [Database migrations](docs/database-migrations.md) and [Application-user resolution](docs/application-user-resolution.md)
- [Firebase authentication setup](docs/firebase-auth-setup.md) and [Firebase authentication testing](docs/firebase-auth-testing.md)
- [Local photo storage](docs/local-photo-storage.md) and [Photo processing](docs/photo-processing.md)
- [Place resolution](docs/place-resolution.md), [Photo metadata](docs/photo-metadata.md) and [Experiments](docs/photo-metadata-experiments.md)
- [Trip persistence](docs/trip-persistence.md), [Manual moderation](docs/moderation.md) and [Development reference](docs/development.md)
- [Coding conventions](docs/coding-conventions.md) and [Deployment](deploy/README.md)

The [frontend repository](https://github.com/TravelMapMory/mapmory-frontend) documents browser behavior and client-side setup.
