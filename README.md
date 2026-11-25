# MapMory backend

Go HTTP API for MapMory, a photo-first travel-memory application. In PostgreSQL database mode, it supports Firebase-authenticated user and trip persistence, photo upload and processing, place resolution and correction, owner maps and summaries, and link or public photo sharing. The optional in-memory mode retains legacy public prototype trip endpoints only.

- [Frontend](https://github.com/TravelMapMory/mapmory-frontend)
- [OpenAPI contract](openapi/openapi.yaml) — implemented database-mode endpoints and explicitly marked proposals
- [Coding conventions](docs/coding-conventions.md)

## Run locally

Use Go 1.26.1 or the version declared in `go.mod`. Complete [Firebase authentication setup](docs/firebase-auth-setup.md) for access to the shared `mapmory-development` project and keyless credentials.

From the repository root:

```bash
cp -n .env.example .env
chmod 600 .env
# Edit .env; FIREBASE_PROJECT_ID is prefilled for the shared development project.
go run ./cmd/server
```

The server reads its allowlisted configuration keys from the repository-root `.env`: `FIREBASE_PROJECT_ID`, `MAPMORY_ENABLE_DB_USER_RESOLUTION`, `DATABASE_URL`, `MAPMORY_PHOTO_STORAGE`, `MAPMORY_LOCAL_PHOTO_DIR`, `MAPMORY_PHOTO_BUCKET`, `MAPMORY_UPLOAD_SIGNER`, and `GEOAPIFY_API_KEY`. Explicitly exported environment variables override the file, and a missing `.env` is fine when configuration is supplied by the deployment environment. `.env` is ignored by Git. The Firebase test token helper also uses `.env`, but the backend does not import test passwords or tokens into its environment.

By default, the server listens at `http://localhost:8080` (override with `PORT`). Without PostgreSQL enabled, `/api/auth/me` remains available but `/api/users/me` is not registered.

To enable `/api/users/me` and **authenticated persistent trips**, follow [Application-user resolution](docs/application-user-resolution.md) to start local PostgreSQL, apply migrations, and set `MAPMORY_ENABLE_DB_USER_RESOLUTION=true` in `.env`. The server requires a working `DATABASE_URL` when this flag is enabled and exits at startup if PostgreSQL is unreachable; it never falls back to memory. It does not apply migrations; run Goose yourself ([Database migrations](docs/database-migrations.md)).

Photo upload, retrieval, owner overview and sharing routes require **configured photo storage** in database mode. For local development without a GCS bucket, set `MAPMORY_PHOTO_STORAGE=local`, `MAPMORY_LOCAL_PHOTO_DIR=.local-photos`, and clear `MAPMORY_PHOTO_BUCKET`; see [Local photo storage](docs/local-photo-storage.md). Production GCS mode still requires a bucket and appropriate IAM permissions. Apply migrations through `00007_create_shares.sql` before enabling sharing.

### PostgreSQL or in-memory?

| Workflow | Needs PostgreSQL? |
| --- | --- |
| `go run ./cmd/server` with `MAPMORY_ENABLE_DB_USER_RESOLUTION` unset or `false` | No. `/healthz` and `/api/auth/me` work; `/api/trips` is the public in-memory prototype, lost on restart; `/api/users/me` returns `404`. |
| `MAPMORY_ENABLE_DB_USER_RESOLUTION=true` | Yes: migrated PostgreSQL at `DATABASE_URL`. Enables `/api/users/me` and persistent, owner-scoped `/api/trips`. |
| `go test ./...`, `go vet ./...`, `go build ./...` | No. Tests use fakes and need neither a database nor Firebase credentials. |
| `./scripts/test-migrations.sh`, `./scripts/test-postgres-integration.sh` | Docker and Goose, but no existing database: each starts and removes its own temporary container and ignores `DATABASE_URL`. |

Every server start still needs Application Default Credentials (see Firebase setup above), even in in-memory mode.

```bash
curl -i http://localhost:8080/healthz       # 200
curl -i http://localhost:8080/api/auth/me   # 401 without a Firebase ID token
```

For a real Firebase ID token, use [Firebase authentication testing](docs/firebase-auth-testing.md) and the examples in [requests.http](requests.http).

## Implemented behavior

| Capability | Current status |
| --- | --- |
| `GET /healthz` | Public health check |
| `GET /api/auth/me` | Firebase ID-token verification; returns verified UID |
| `GET /api/users/me` | Opt-in: resolves Firebase UID to PostgreSQL user UUID; creates a user if missing |
| `GET /api/trips`, `POST /api/trips`, `GET /api/trips/{trip_id}` | Database mode: Firebase-authenticated, owner-scoped PostgreSQL trips; legacy mode: public in-memory prototype |
| Photo metadata and display-image processing | Upload mode: extracts available EXIF metadata, normalizes photo information, generates an EXIF-free display JPEG and stores processing results. Local inspection tools are also available. |
| Place labels on upload | Database mode with `GEOAPIFY_API_KEY`: a processed photo with GPS gets suggested country, city and place labels; without the key, or when Geoapify fails, labels stay `pending` and the photo is unaffected |
| `POST /api/trips/{trip_id}/batches` | Database mode with configured photo storage: registers JPEG/PNG files as pending photos and returns signed `PUT` URLs (GCS or loopback local storage) |
| `POST /api/batches/{batch_id}/files/{client_file_id}/complete` | Same mode: checks the uploaded object, extracts EXIF, stores an EXIF-free display JPEG and returns the photo |
| `GET /api/batches/{batch_id}`, `GET /api/trips/{trip_id}/photos`, `GET /api/photos/{photo_id}` | Same mode: owner-only batch status, photo list in capture order (cursor-paginated) and single photo, with signed display-image URLs |
| `PATCH /api/photos/{photo_id}/location` | Upload mode: owner corrects a photo's position (labels resolved and confirmed; EXIF and capture time unchanged) or confirms suggested labels |
| `GET /api/places/search?q=` | Database mode, public and rate-limited: world cities and countries for Find a place and corrections; `503` without `GEOAPIFY_API_KEY` |
| `GET /api/map/photos`, `GET /api/me/summary` | Upload mode: owner map markers (bbox, trip, date and text filters; at most 2,000) and Dashboard totals with recent uploads |
| Trip aggregates | Upload mode: trip responses include photo and located counts, cities, countries, date range and a signed cover |
| `DELETE /api/trips/{trip_id}`, `DELETE /api/me` | Database mode: delete one trip (photos, batches, shares, stored images) or all of the caller's data and their application-user record |
| Link and public shares | Upload mode: owners create, list and revoke fixed selections of processed photos. `GET /api/s/{token}` is accessible to anyone holding the link (410 after revocation); public shares also appear at `GET /api/public/shares/{share_id}` and on the clustered `GET /api/public/map`. Recipients receive selected photos, signed display images and stored coarse positions (city centres or 0.01° rounding), not original EXIF coordinates. Revocation blocks subsequent lookups but does not invalidate previously issued signed URLs or necessarily stop in-flight requests. |

**Security:** Enable `MAPMORY_ENABLE_DB_USER_RESOLUTION=true` to use protected, persistent trip routes. When disabled, trips are **public, shared in memory and lost on restart**; never use the prototype for private information. See [Trip persistence](docs/trip-persistence.md) for ownership and remaining limitations. The OpenAPI document also contains unimplemented proposed endpoints.

## Development guides

- [Local photo storage](docs/local-photo-storage.md) — upload, processing and sharing without GCS
- [Development reference](docs/development.md) — endpoints, components, and test commands
- [Firebase authentication setup](docs/firebase-auth-setup.md) — access and keyless ADC
- [Firebase authentication testing](docs/firebase-auth-testing.md) — disposable account, token helper, REST Client
- [Trip persistence](docs/trip-persistence.md) — authenticated routes, ownership and end-to-end tests
- [Application-user resolution](docs/application-user-resolution.md) — local PostgreSQL, provisioning, and API verification
- [Database migrations](docs/database-migrations.md) — Goose schema management and lifecycle tests
- [Photo metadata](docs/photo-metadata.md) and [experiments](docs/photo-metadata-experiments.md)
- [Photo processing](docs/photo-processing.md) and [place resolution](docs/place-resolution.md)
- [Deployment](deploy/README.md)

## Checks

```bash
go test ./...
go vet ./...
go build ./...
./scripts/test-postgres-integration.sh  # Docker + Goose required
```

The ordinary Go tests do not need a running database. For Docker image construction, run `docker build -t mapmory-backend .`; runtime credentials and configuration are still required. See the deployment guide for the VM setup. Database-backed repositories, sharing and migration lifecycle have been verified locally with isolated PostgreSQL. Production end-to-end verification still requires deployed PostgreSQL configuration, GCS signing permissions and browser-level upload/sharing checks. The current VM configuration does not yet enable database mode; see the deployment guide.
