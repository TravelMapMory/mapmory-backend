# Firebase Authentication — testing

This guide covers local/manual verification of Firebase ID tokens and automated Go tests. For project access, keyless ADC and backend startup, start with [Firebase Authentication — local development](firebase-auth-setup.md).

**Project:** `mapmory-development`  
**Scope:** `GET /api/auth/me` only. Existing in-memory trip endpoints are **not** authenticated or owner-secured.

## 1. Verify the running API

Start the Go backend in one terminal following the setup guide:

```bash
export FIREBASE_PROJECT_ID=mapmory-development
unset FIREBASE_AUTH_EMULATOR_HOST
unset GOOGLE_APPLICATION_CREDENTIALS
go run ./cmd/server
```

From another terminal:

```bash
curl -i http://localhost:8080/healthz       # 200
curl -i http://localhost:8080/api/auth/me   # 401
curl -i http://localhost:8080/api/auth/me \
  -H 'Authorization: Bearer invalid'       # 401
```

## 2. Prepare a local test account and `.env`

Use a **disposable Firebase test account** belonging to you, created through an authorized registration flow or provisioned by the Firebase project administrator. Do not use your personal Google account password or a real user's account. You do **not** need Firebase Console access to sign in with an existing test account.

The Firebase Web API key is public Firebase web-app configuration. Obtain it from the project administrator; it is **not** a Google service-account credential.

The `.env` file is **not created automatically** by the backend or the token helper. Before modifying it, check whether it already exists in the **backend repository root**. Do not overwrite an existing file: preserve other local settings. Confirm it is ignored by Git (`git check-ignore .env`). If you need to create it, use `umask 077` and `cp -n .env.example .env` from the repository root (or create it securely with an editor). In either case, run `chmod 600 .env` on Unix-like systems. The helper preserves existing file permissions; it **does not correct an overly permissive mode**.

Add or update these entries in your **local, ignored** `.env`:

```dotenv
FIREBASE_WEB_API_KEY=your-firebase-web-api-key
TEST_EMAIL=your-disposable-test-user@example.com
TEST_PASSWORD=your-disposable-test-user-password
```

**Security:** `TEST_PASSWORD` is stored in plaintext. This is a convenience for disposable test-only credentials, not a recommendation for real accounts. Never commit or share `.env`. If you prefer not to store even test passwords on disk, use your shell's interactive input or an OS credential manager instead. The backend itself does not load this `.env` file.

## 3. Sign in and obtain a Firebase ID token

### Shortcut: `scripts/firebase-id-token.sh`

Once the `.env` from section 2 exists, one command signs in and saves a fresh token as `FIREBASE_ID_TOKEN` in it. This is an alternative to the manual sign-in steps below. The saved token is available to `requests.http` (section 4); for command-line `curl`, use `--print` to export it to your current shell:

```bash
scripts/firebase-id-token.sh                                 # sign in, update FIREBASE_ID_TOKEN in .env
scripts/firebase-id-token.sh --check http://localhost:8080   # ...then call GET /api/auth/me (200)
scripts/firebase-id-token.sh --check https://mapmory.app     # ...against a deployed backend
export FIREBASE_ID_TOKEN="$(scripts/firebase-id-token.sh --print)" # also hand the token to this shell
```

The script needs `curl` and `jq`. It reads `FIREBASE_WEB_API_KEY`, `TEST_EMAIL` and `TEST_PASSWORD` from `.env` without sourcing it, and it never creates `.env`: if the file or a variable is missing it stops and says what to add. The first `FIREBASE_ID_TOKEN` line is replaced (or a line is appended if absent), and any duplicate token entries are removed; other configuration lines and the file mode are kept. The helper does not enforce restrictive permissions, so check with `ls -l .env` and use `chmod 600 .env` if needed. It never prints the password or the API key, and prints the token only if you pass `--print` (then only the token goes to stdout and all messages to stderr). On failure it shows Firebase's error message, for example `INVALID_LOGIN_CREDENTIALS`. With `--check`, the token is **saved before** the API request, so a connection failure or non-200 response does not undo a successful sign-in or roll back `.env`. The command exits non-zero unless `/api/auth/me` returns 200; its current implementation also prints the response body, so avoid using it against endpoints returning sensitive data.

To sign in against the Firebase Auth emulator instead of the real project, set `FIREBASE_AUTH_EMULATOR_HOST` (for example `127.0.0.1:9099`) first. The script prints which endpoint it uses; `unset FIREBASE_AUTH_EMULATOR_HOST` to go back to the real project.

### Manual steps

The commands below use `jq`, `curl`, and POSIX-compatible shell syntax. Read the values from `.env` without sourcing the file as executable shell code. They assume each variable appears once as a simple `KEY=value` line and that the test password does not contain newline characters.

```bash
FIREBASE_WEB_API_KEY=$(sed -n 's/^FIREBASE_WEB_API_KEY=//p' .env | tail -n 1)
TEST_EMAIL=$(sed -n 's/^TEST_EMAIL=//p' .env | tail -n 1)
TEST_PASSWORD=$(sed -n 's/^TEST_PASSWORD=//p' .env | tail -n 1)

test -n "$FIREBASE_WEB_API_KEY" && test -n "$TEST_EMAIL" && test -n "$TEST_PASSWORD" \
  && echo "Firebase test configuration loaded"

RESPONSE=$(jq -n --arg email "$TEST_EMAIL" --arg password "$TEST_PASSWORD" \
  '{email: $email, password: $password, returnSecureToken: true}' | \
  curl -fsS \
    "https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=${FIREBASE_WEB_API_KEY}" \
    -H 'Content-Type: application/json' --data-binary @-)
FIREBASE_ID_TOKEN=$(printf '%s' "$RESPONSE" | jq -r '.idToken // empty')
test -n "$FIREBASE_ID_TOKEN" && echo 'Firebase ID token obtained'

curl -i http://localhost:8080/api/auth/me \
  -H "Authorization: Bearer ${FIREBASE_ID_TOKEN}"
```

The response should be HTTP **200** with `{"uid":"..."}`, matching the user's Firebase UID. The token was issued by **Firebase Authentication**, not by Google Cloud service-account impersonation. Do not print or share it.

## 4. Use `requests.http` in VS Code

The REST Client's authenticated request uses `{{$dotenv FIREBASE_ID_TOKEN}}` from the repository-root `.env`. It does not read your terminal's `FIREBASE_ID_TOKEN` automatically.

The shortcut in section 3 writes this entry for you. To do it by hand instead, **before writing the token** check whether `.env` exists and preserve every existing entry. Since it already contains the Web API key and test credentials, edit the file and add or replace only the line below, using the token held in your shell:

```dotenv
FIREBASE_ID_TOKEN=your-newly-obtained-firebase-id-token
```

Do not copy this token into tracked files or a GitHub comment. Do not use `> .env`, as that overwrites the other entries. In VS Code, run **Authentication — valid Firebase user ID token (200)** from [`requests.http`](../requests.http).

ID tokens typically expire after about an hour; rerun the helper (or sign in manually) to replace the entry when necessary. Remove the `FIREBASE_ID_TOKEN` line after testing. The Web API key may remain, but remove disposable test credentials when no longer needed.

In your shell, clear values after finishing:

```bash
unset RESPONSE FIREBASE_ID_TOKEN TEST_EMAIL TEST_PASSWORD FIREBASE_WEB_API_KEY
```

**Never** paste the OAuth access token from `gcloud auth application-default print-access-token` into the API's `Authorization` header. It is a Google Cloud token, not a Firebase user ID token.

## 5. Automated tests

The middleware tests in `internal/auth` use a fake verifier, and `cmd/server/auth_route_test.go` tests route wiring and 401/200 responses. Neither needs Firebase credentials or network access.

```bash
go test ./cmd/server -run '^TestAuthMeRoute$' -v
go test ./...
go test -race ./...
go vet ./...
```

## What is not covered

`GET /api/auth/me` demonstrates verified identity only. It does not resolve a PostgreSQL user or enforce trip ownership. Ordinary Firebase `VerifyIDToken` does not check token revocation. These concerns belong to subsequent issues under backend #8.
