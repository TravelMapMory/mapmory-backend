# Local photo storage (development only)

The backend can run the **real authenticated, PostgreSQL-backed upload → process → share → revoke flow** without Google Cloud Storage. Local mode implements the same `photostorage.Store` interface as GCS: registered uploads receive a temporary PUT URL; completion reads the original, processes it, and writes a display JPEG; photo and share responses receive temporary display-image URLs.

Local mode is **not** a mock backend: it uses real Firebase verification, user/trip ownership, PostgreSQL migrations and the photo-processing pipeline. Only object storage and URL signing are local. It does not seed demonstration photos. The frontend's mock dataset remains available only when `VITE_API_URL` is unset.

## Prerequisites

- Go as declared in `go.mod`.
- Docker/PostgreSQL 17 and Goose. Follow [Application-user resolution](application-user-resolution.md) to start PostgreSQL and [Database migrations](database-migrations.md) to apply all current migrations (through `00009`).
- Firebase development project credentials, as documented in [Firebase authentication setup](firebase-auth-setup.md). Local photo storage removes **GCS** bucket and IAM signing requirements, not Firebase authentication.
- A frontend Vite server at `http://localhost:5173` or `http://127.0.0.1:5173` (also permits the configured local preview ports 5174 and 4173).

## Backend configuration

Create or edit the backend's ignored `.env` (do not commit it):

```dotenv
FIREBASE_PROJECT_ID=mapmory-development
MAPMORY_ENABLE_DB_USER_RESOLUTION=true
DATABASE_URL=postgres://mapmory:mapmory-local@127.0.0.1:5432/mapmory?sslmode=disable

MAPMORY_PHOTO_STORAGE=local
MAPMORY_LOCAL_PHOTO_DIR=.local-photos
MAPMORY_PHOTO_BUCKET=
MAPMORY_UPLOAD_SIGNER=

# Optional. Without Geoapify, photos can still be uploaded and shared,
# but automatic place labels and world-place search are unavailable.
GEOAPIFY_API_KEY=
```

**Important:** `MAPMORY_PHOTO_BUCKET` must be empty in local mode, even if `.env.example` originally had a bucket value. Exported environment variables override `.env`, so run `unset MAPMORY_PHOTO_BUCKET MAPMORY_PHOTO_STORAGE MAPMORY_LOCAL_PHOTO_DIR` in your shell if earlier exports conflict with the file.

Start the backend from the repository root:

```bash
go run ./cmd/server
```

In local photo mode it binds **only to `127.0.0.1:8080`** (or `127.0.0.1:$PORT`), never to a public interface. Files are stored in `.local-photos/`, which is Git-ignored and must be kept private. A fresh server start generates a new signing key: already issued local URLs stop working, but photos persist and the API can issue fresh display URLs.

## Frontend configuration

Use a frontend `.env.local` containing:

```dotenv
VITE_API_URL=http://127.0.0.1:8080
```

Restart Vite after editing its environment. The browser sends authenticated API requests to the backend and uploads photo bytes to the local signed PUT URL. The dev-only CORS policy permits requests from loopback Vite origins on ports 5173, 5174 and 4173. It is not enabled for GCS mode.

## End-to-end check

1. Verify the backend is running: `curl http://127.0.0.1:8080/healthz`.
2. Sign in from the frontend using a Firebase test account; create a trip.
3. Upload a JPEG or PNG under 25 MB, and check that processing completes. A real original and display JPEG should appear under `.local-photos/originals/` and `.local-photos/display/`.
4. Reload the trip. Its persisted photos should still be present, with display images loading from signed loopback URLs.
5. Create a **link share** of one processed photo; open the recipient URL in a private browser window without signing in.
6. Revoke the share. A fresh recipient request should return the revoked state. A previously issued display URL might still work until its one-hour expiry; this is the same limitation as production signed GCS URLs.
7. Create a **public share** and confirm it can be found on the public map. Revoke it and check that it disappears.

If a photo has no location labels, the recipient map uses the coarse-coordinate fallback. For testing world-place search and geocoding, configure `GEOAPIFY_API_KEY` separately.

## Security and limitations

- **Never deploy local storage publicly.** This mode binds to loopback and is intended for a single developer only.
- Original files are not exposed by signed GET URLs; only processed `display/` objects can receive those URLs. Uploaded originals can only be read by the backend through its store interface.
- PUT URLs are time-limited (15 minutes) and bound to an object, content type and exact byte length. Display URLs expire after one hour. Tokens are HMAC-authenticated with an ephemeral server key.
- The filesystem is not a GCS emulator, does not provide cloud durability, and is not suitable for multi-instance deployment. Data is retained locally until removed manually. Deleting a database record does **not** automatically remove all local files; this is development data only.
- GCS mode remains the default when `MAPMORY_PHOTO_STORAGE` is unset or `gcs` and a bucket is configured; this mode does not alter deployed GCS storage behavior.
