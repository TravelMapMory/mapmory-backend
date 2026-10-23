# MapMory backend

Go HTTP API for MapMory, a photo-first travel-memory application. The API currently has Firebase authentication, optional PostgreSQL-backed application-user resolution, and prototype trip endpoints. Photo processing and place resolution exist as libraries but are not yet integrated into the API.

- [Frontend](https://github.com/TravelMapMory/mapmory-frontend)
- [OpenAPI draft](openapi/openapi.yaml) — planned behavior, not all implemented
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

The server reads **only its three configuration keys** from the repository-root `.env`: `FIREBASE_PROJECT_ID`, `MAPMORY_ENABLE_DB_USER_RESOLUTION`, and `DATABASE_URL`. Explicitly exported environment variables override the file, and a missing `.env` is fine when configuration is supplied by the deployment environment. `.env` is ignored by Git. The Firebase test token helper also uses `.env`, but the backend does not import test passwords or tokens into its environment.

By default, the server listens at `http://localhost:8080` (override with `PORT`). Without PostgreSQL enabled, `/api/auth/me` remains available but `/api/users/me` is not registered.

To enable `/api/users/me`, follow [Application-user resolution](docs/application-user-resolution.md) to start local PostgreSQL, apply migrations, and set `MAPMORY_ENABLE_DB_USER_RESOLUTION=true` in `.env`. The server requires a working `DATABASE_URL` when this flag is enabled.

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
| `GET /api/trips`, `POST /api/trips`, `GET /api/trips/{trip_id}` | Public prototype; process-wide in-memory storage, no ownership checks |
| Photo metadata and display-image processing | Libraries and local tools only |
| Geoapify place resolution | Library only |
| Photo upload, trip persistence, sharing | Not implemented |

**Security:** The trip endpoints remain **unauthenticated and unscoped**. Do not use this prototype to store private user data. The OpenAPI draft describes protections that are not yet implemented.

## Development guides

- [Development reference](docs/development.md) — endpoints, components, and test commands
- [Firebase authentication setup](docs/firebase-auth-setup.md) — access and keyless ADC
- [Firebase authentication testing](docs/firebase-auth-testing.md) — disposable account, token helper, REST Client
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

The ordinary Go tests do not need a running database. For Docker image construction, run `docker build -t mapmory-backend .`; runtime credentials and configuration are still required. See the deployment guide for the VM setup. Database-backed user resolution has been verified locally but is not yet enabled in the VM deployment.
