# Environment and local setup

Choose a **scenario** below before setting variables. This guide covers the Go API; the [frontend environment guide](https://github.com/TravelMapMory/mapmory-frontend/blob/main/docs/environment-setup.md) uses the same scenario labels. Commands assume you are in the **backend repository root** unless marked otherwise.

## Choose a scenario

| Scenario | What runs | Authentication | Database / photos | Start here |
| --- | --- | --- | --- | --- |
| **A. Mock UI only** | Frontend only | Demo sign-in | Frontend mock data | [Frontend guide, A](https://github.com/TravelMapMory/mapmory-frontend/blob/main/docs/environment-setup.md#scenario-a--mock-ui-only) — no backend required |
| **B. Auth UI test** | Frontend + Firebase Auth emulator | Emulator sign-in | Frontend mock data | [Frontend guide, B](https://github.com/TravelMapMory/mapmory-frontend/blob/main/docs/environment-setup.md#scenario-b--firebase-auth-ui-with-mock-data) — no backend required |
| **C. Full local, emulated auth** | Frontend + Go API + PostgreSQL + Auth emulator | Emulator users/tokens | Local photo filesystem | [Scenario C](#scenario-c--full-local-stack-with-firebase-auth-emulator) |
| **D. Full local, real auth** | Frontend + Go API + PostgreSQL | Real Firebase `mapmory-development` | Local photo filesystem | [Scenario D](#scenario-d--full-local-stack-with-real-firebase) |
| **E. GCS-backed integration** | Same as C or D, with GCS | Match C or D | Private Google Cloud Storage | [Scenario E](#scenario-e--use-private-gcs-instead-of-local-photo-storage) |
| **F. Backend tests** | Go / optional disposable DB | Test fakes where possible | Depends on test | [Testing](#testing-by-dependency) |

**Do not run the legacy public in-memory backend to validate owner-scoped trips or sharing.** It is a historical prototype, not the frontend's mock mode.

## External services and credentials for a full setup

**Choose your dependencies before editing `.env`.** A full local upload → correct → share workflow requires Firebase authentication (real or emulated), PostgreSQL, the Go API, and a photo store. Geoapify is optional for basic uploads but necessary to exercise automatic place labels and world-place search. Cloud Storage is **not** required when using the local photo store.

| Service or resource | Why MapMory uses it | Scenario C (local default) | Scenario D (real Firebase) | Scenario E (GCS) | Where to configure / obtain it |
| --- | --- | --- | --- | --- | --- |
| **Firebase Authentication** | User sign-in; Go API verifies Firebase ID tokens | Run local Auth emulator; use `demo-mapmory` | Use the team's **development** Firebase project | Same as C or D | Backend `.env`: `FIREBASE_PROJECT_ID`; frontend `.env.local`: four `VITE_FIREBASE_*` Web app identifiers and, for emulator, `VITE_FIREBASE_AUTH_EMULATOR_URL`. For real Firebase, obtain Web app settings from **Firebase Console → Project settings → General → Your apps**; enable the intended sign-in method in Authentication. |
| **Firebase Admin / Google credentials** | Initialize the Go Firebase Admin SDK for real-project verification | Emulator mode; no real user tokens | Developer must have authorized keyless ADC / service-account impersonation | Also needed for Google Cloud access/signing as appropriate | Configure with `gcloud auth application-default login --impersonate-service-account=...` as described in [Firebase setup](firebase-auth-setup.md). ADC is **not** the Firebase Web API key or a user ID token. |
| **PostgreSQL 17** | Persistent users, trips, photos, and shares | Local Docker container + Goose migrations | Same | Local or approved managed database | Set backend `.env` `DATABASE_URL`; run migrations before the Go server. Never use a shared production DB for local experiments. |
| **Supabase (managed PostgreSQL)** | Hosts MapMory's deployed PostgreSQL database; provides PostgreSQL rather than a separate database type | Not needed | Not needed | Optional alternative to local PostgreSQL, only with an authorized *development* database | Use a development PostgreSQL connection string as `DATABASE_URL` and apply the same Goose migrations. Do not copy the production Supabase connection string into local `.env`, or run migrations against production. |
| **Photo storage** | Original JPEG/PNG bytes and processed display images | Local filesystem | Local filesystem | **Private GCS bucket** | Local: `MAPMORY_PHOTO_STORAGE=local`, `MAPMORY_LOCAL_PHOTO_DIR=.local-photos`, empty `MAPMORY_PHOTO_BUCKET`. GCS: `MAPMORY_PHOTO_STORAGE=gcs`, `MAPMORY_PHOTO_BUCKET=<your development bucket>`, appropriate ADC/IAM, signed-URL permissions and bucket CORS. Obtain bucket access from the team/cloud administrator or provision an authorized development bucket; setting its name does not create it. |
| **Geoapify geocoding API** | Reverse geocoding after upload; world-place search and location correction suggestions | Optional for uploads; **needed to test place features** | Same | Same | Create/use a Geoapify account and project, obtain an API key from its dashboard, put it **only in backend `.env`** as `GEOAPIFY_API_KEY`. Without it, photos still upload but labels may stay `pending` and place search is unavailable. See [Place resolution](place-resolution.md). |
| **Geoapify map tiles** | Optional browser basemap styles | Optional | Optional | Optional | A separate browser-exposed key goes in frontend `.env.local` as `VITE_GEOAPIFY_KEY`; it is **not** the backend `GEOAPIFY_API_KEY`. Empty uses OpenStreetMap-based fallback tiles. Restrict browser key usage by allowed origins where the provider permits. |

**Internal and external contributors.** Scenario **C** is the recommended starting point for everyone: it uses local PostgreSQL, local photo storage, and the Firebase Auth emulator without access to the team's cloud projects. Authorized team members can use **D** with `mapmory-development` and the listed impersonation identity. External contributors should instead create their **own Firebase development project**, configure the frontend Web app settings, set backend `FIREBASE_PROJECT_ID` to the same project ID, and supply their own appropriate Firebase Admin credentials/ADC. The team-specific service account below will not work without granted IAM access. Do not use production accounts or credentials.

**What to request from a teammate:** For team-managed **D**, request the development Firebase Web app settings and authorized ADC/impersonation identity. For **E**, additionally request the **development** GCS bucket name, relevant IAM/signing permissions and CORS configuration. For full geographic testing in any live scenario, obtain a **separate backend Geoapify key**. A managed Supabase database is optional for local work; if testing against one, request a disposable development database URL, not production access.

### Before running scenario C, D, or E

- Confirm that the frontend Firebase project ID matches backend `FIREBASE_PROJECT_ID`, and that emulator settings are enabled **on both sides or neither**.
- Confirm `DATABASE_URL` points to a running PostgreSQL instance and that Goose migrations have been applied.
- Choose **exactly one** photo store: local filesystem for C/D, or an authorized private GCS bucket for E.
- Decide whether to test geographic features: set backend `GEOAPIFY_API_KEY` for labels and place search; optionally set frontend `VITE_GEOAPIFY_KEY` for tiles.
- Start the backend before the live frontend and verify `GET /healthz`; then sign in and test a JPEG/PNG upload, location correction and sharing.

## Prerequisites and environment files

- Go **1.26.1** (`go.mod`); Docker for the PostgreSQL 17 development container; Goose **v3.29.0** for schema migrations.
- Firebase CLI for scenario C; Google Cloud CLI and authorized keyless Application Default Credentials (ADC) for scenario D and some GCS configurations.
- Start with a local, ignored backend `.env` (do not overwrite existing personal settings):

```bash
# Backend repository root — once
cp -n .env.example .env
chmod 600 .env
```

Edit `.env` as indicated below. The Go server reads **eight** allowlisted settings from it. Exported process variables take precedence, **even if exported as empty strings**. `FIREBASE_AUTH_EMULATOR_HOST` and `PORT` are process-only variables, **not** loaded from `.env`. The Firebase Web API key and test account fields at the bottom of `.env.example` are used only by [the token helper](firebase-auth-testing.md), not by the Go server. Never commit `.env`.

### Backend settings by scenario

| `.env` setting | C — emulator + local photos | D — real Firebase + local photos | E — GCS instead of local |
| --- | --- | --- | --- |
| `FIREBASE_PROJECT_ID` | `demo-mapmory` | `mapmory-development` | Same as chosen auth mode |
| `MAPMORY_ENABLE_DB_USER_RESOLUTION` | `true` | `true` | `true` |
| `DATABASE_URL` | Local migrated PostgreSQL URL | Same | Local or authorized migrated PostgreSQL URL |
| `MAPMORY_PHOTO_STORAGE` | `local` | `local` | `gcs` |
| `MAPMORY_LOCAL_PHOTO_DIR` | `.local-photos` | `.local-photos` | Unused |
| `MAPMORY_PHOTO_BUCKET` | **Empty** | **Empty** | Private GCS bucket name |
| `MAPMORY_UPLOAD_SIGNER` | Empty | Empty | Only if required by signing setup |
| `GEOAPIFY_API_KEY` | Optional | Optional | Optional |

`GEOAPIFY_API_KEY` is a **backend secret**; see the [external services table](#external-services-and-credentials-for-a-full-setup) for where to obtain it and how it differs from the browser map-tile key.

## Shared prerequisite for C and D: local PostgreSQL

In **terminal 1**, start a PostgreSQL container on localhost (first run):

```bash
docker run -d --name mapmory-postgres \
  -e POSTGRES_USER=mapmory \
  -e POSTGRES_PASSWORD=mapmory-local \
  -e POSTGRES_DB=mapmory \
  -p 127.0.0.1:5432:5432 \
  -v mapmory-postgres-data:/var/lib/postgresql/data \
  postgres:17
```

On subsequent runs use `docker start mapmory-postgres` **instead of** the `docker run` command. Then wait for readiness (Bash or Zsh):

```bash
ready=false
for attempt in {1..30}; do
  if docker exec mapmory-postgres pg_isready -U mapmory -d mapmory >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [ "$ready" != true ]; then
  echo 'PostgreSQL did not become ready' >&2
  exit 1
fi
```

In the **backend `.env`**, set the database URL and local storage values:

```dotenv
MAPMORY_ENABLE_DB_USER_RESOLUTION=true
DATABASE_URL=postgres://mapmory:mapmory-local@127.0.0.1:5432/mapmory?sslmode=disable
MAPMORY_PHOTO_STORAGE=local
MAPMORY_LOCAL_PHOTO_DIR=.local-photos
MAPMORY_PHOTO_BUCKET=
MAPMORY_UPLOAD_SIGNER=
GEOAPIFY_API_KEY=
```

The password and `sslmode=disable` above are **only** for the loopback-bound development database. If port 5432 is occupied, change the Docker host port and `DATABASE_URL` together.

Apply schema migrations once, and again after pulling new migrations:

```bash
go install github.com/pressly/goose/v3/cmd/goose@v3.29.0
# Goose does not load .env. Read the URL from your own local file.
export DATABASE_URL="$(sed -n 's/^DATABASE_URL=//p' .env | tail -n 1)"
goose -dir db/migrations postgres "$DATABASE_URL" up
goose -dir db/migrations postgres "$DATABASE_URL" status
```

Goose must be on your `PATH` (typically `$(go env GOPATH)/bin`). The backend does **not** run migrations automatically. If you have an older exported `DATABASE_URL`, check that it points to this local container before using Goose or starting the API.

## Scenario C — Full local stack with Firebase Auth emulator

This scenario exercises persistent trips, uploads, location correction, and link/public shares against **local test accounts** rather than the shared Firebase project.

1. Complete [Shared prerequisite](#shared-prerequisite-for-c-and-d-local-postgresql) above.
2. In backend `.env`, set `FIREBASE_PROJECT_ID=demo-mapmory`, plus the common settings above.
3. In **terminal 2**, start the emulator:

   ```bash
   npx firebase-tools emulators:start --only auth --project demo-mapmory
   ```

4. In **terminal 3** (backend root), explicitly configure the Go process, then start the server:

   ```bash
   export FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9099
   go run ./cmd/server
   ```

5. In **terminal 4**, verify its public health endpoint:

   ```bash
   curl -i http://127.0.0.1:8080/healthz
   ```

6. Configure the React frontend using [frontend scenario C](https://github.com/TravelMapMory/mapmory-frontend/blob/main/docs/environment-setup.md#scenario-c--full-local-stack-with-firebase-auth-emulator); its Firebase project ID must also be `demo-mapmory`.

**Expected:** `/healthz` returns `200`; `/api/auth/me` returns `401` without a user token; authenticated application operations work after signing in through the frontend. The Auth emulator issues **test-only tokens**, so never enable this host in a deployed backend. If the Admin SDK in your environment still requires ADC at startup, follow its initialization requirements; do not use production tokens with the emulator.

## Scenario D — Full local stack with real Firebase

This scenario uses a **real Firebase development project** (the team's `mapmory-development` when authorized, or an external contributor's own project), with a local development database and photo store. The steps below illustrate the **team-managed** configuration; substitute your own project ID and credentials consistently if working independently.

1. Complete [Shared prerequisite](#shared-prerequisite-for-c-and-d-local-postgresql).
2. Set `FIREBASE_PROJECT_ID=mapmory-development` in backend `.env` for team-managed Firebase, or set it to your own development Firebase project ID; keep the local PostgreSQL/storage configuration above.
3. Configure Google Cloud CLI and keyless ADC as described in [Firebase authentication setup](firebase-auth-setup.md). **Authorized team members only:** the following impersonation command requires permission to act as the named service account. External contributors must configure ADC for their own Firebase project instead:

   ```bash
   gcloud auth application-default login \
     --impersonate-service-account=mapmory-local-dev@mapmory-development.iam.gserviceaccount.com
   gcloud auth application-default print-access-token >/dev/null && echo 'ADC ready'
   ```

4. In **terminal 2** (backend root), make sure the emulator is disabled before launching Go:

   ```bash
   unset FIREBASE_AUTH_EMULATOR_HOST
   unset GOOGLE_APPLICATION_CREDENTIALS  # use impersonated ADC, not a local service-account key
   go run ./cmd/server
   ```

5. In another terminal, confirm `curl -i http://127.0.0.1:8080/healthz` returns `200`, then configure [frontend scenario D](https://github.com/TravelMapMory/mapmory-frontend/blob/main/docs/environment-setup.md#scenario-d--full-local-stack-with-real-firebase) using Web SDK settings for **the same Firebase project**. If using your own project, replace `mapmory-development` in both configuration files.

**Important:** Google ADC identifies the developer/backend to Google Cloud; it is **not** the Firebase ID token sent by a signed-in MapMory user. Get test-user credentials separately; see [Firebase authentication testing](firebase-auth-testing.md).

## Scenario E — Use private GCS instead of local photo storage

Start with C or D, including PostgreSQL and the matching Firebase setup. Change the **backend `.env`** photo settings to:

```dotenv
MAPMORY_PHOTO_STORAGE=gcs
MAPMORY_PHOTO_BUCKET=your-private-development-bucket
MAPMORY_UPLOAD_SIGNER=
```

The local photo directory is unused. You must additionally supply appropriate Google ADC, GCS object permissions, signing rights, and **bucket CORS** allowing the chosen frontend origin for signed browser uploads. These are **external cloud prerequisites**, not something the `.env` values create. Follow [Deployment](../deploy/README.md) for identity and signing details. Do not copy production credentials into a local development file or make the photo bucket public.

Run `go run ./cmd/server` after changing the configuration. The frontend still uses scenario C or D (matching Firebase project, `VITE_API_URL=http://127.0.0.1:8080`). Local-photo CORS restrictions do not configure GCS bucket CORS automatically.

## Legacy prototype (not an end-to-end setup)

For debugging only, set `MAPMORY_ENABLE_DB_USER_RESOLUTION=false` in backend `.env` and configure Firebase initialization as above; then run `go run ./cmd/server`. Trips are **public, in-memory, and lost on restart**; `/api/users/me` and persistent upload/share functionality are absent. Do not connect this mode to real personal content. For purely mocked UI work, use frontend scenario A instead.

## Testing by dependency

| Test | Needs running DB / Firebase / frontend? | Command (backend root) |
| --- | --- | --- |
| Unit, fake-backed HTTP, build | No live services | `go test ./...` ; `go vet ./...` ; `go build ./...` |
| Disposable PostgreSQL integration | Docker and Goose | `./scripts/test-postgres-integration.sh` |
| Migration round-trip | Docker and Goose | `./scripts/test-migrations.sh` |
| Manual authenticated API smoke | Scenario C or D running | [Token helper and REST requests](firebase-auth-testing.md) |
| End-to-end browser flow | Scenarios C or D, plus frontend | [Frontend smoke checklist](https://github.com/TravelMapMory/mapmory-frontend/blob/main/docs/live-backend-smoke-test.md) |

The integration scripts create and dispose of **their own** PostgreSQL containers. The frontend smoke test should include one GPS-tagged and one unlocated JPEG/PNG, location correction, refresh/persistence, link-share access signed out, and revocation. Public-share creation requires a verified email address.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Server cannot reach PostgreSQL | Container ready? Matching `DATABASE_URL`? Goose migrations applied? Exported env overriding `.env`? |
| `/api/users/me` is `404` | Database user resolution is disabled; use `true` for C–E. |
| Authenticated endpoints return `401` | Frontend/backend Firebase project mismatch, expired ID token, or real/emulated auth mismatch. |
| Upload/share routes missing | Persistent database mode **and** configured photo storage are required. |
| Local photo store fails at startup | `MAPMORY_PHOTO_BUCKET` must be empty, including exported values. |
| Signed photo URLs fail locally | Use Vite `5173` for the full app. Local storage CORS allows `5173`, `5174`, `4173`, not auth-only test port `5180`. Check `PORT`, file directory, expiration, and content type. |
| Place labels or search unavailable | Set backend `GEOAPIFY_API_KEY`; it is distinct from frontend `VITE_GEOAPIFY_KEY`. |
| Old signed photo URLs fail after restart | Local photo store uses a fresh ephemeral signing key; request new URLs. |
| Goose not found | Add `$(go env GOPATH)/bin` to `PATH`; Goose does not load `.env` automatically. |

See also [Database migrations](database-migrations.md), [Local photo storage](local-photo-storage.md), and [Application-user resolution](application-user-resolution.md).
