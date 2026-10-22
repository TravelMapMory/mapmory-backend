#!/usr/bin/env bash
# Sign in to Firebase Authentication with the disposable test account from the
# repository-root .env and store a fresh ID token there as FIREBASE_ID_TOKEN,
# so requests.http and curl work again after the one-hour expiry.
# .env is parsed, never sourced or created, and all its other lines are kept.
# The password, API key and token are not printed unless --print is given.
# Set FIREBASE_AUTH_EMULATOR_HOST (host:port) to use the Firebase Auth emulator.
# See docs/firebase-auth-testing.md.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT_DIR/.env"

PRINT_TOKEN=false
CHECK_URL=""
TMP_ENV=""

# Print the command-line synopsis.
usage() {
  cat <<'EOF'
Usage: scripts/firebase-id-token.sh [--print] [--check BASE_URL]

Signs in with FIREBASE_WEB_API_KEY, TEST_EMAIL and TEST_PASSWORD from the
repository-root .env and saves the new token as FIREBASE_ID_TOKEN in it.

  --print           also print the token (and only the token) to stdout;
                    all messages go to stderr
  --check BASE_URL  call GET BASE_URL/api/auth/me with the new token
EOF
}

# Print an informational message.
log() {
  printf '%s\n' "$*"
}

# Print an error message to stderr and abort.
fail() {
  printf '%s\n' "$*" >&2
  exit 1
}

# Remove the temporary env copy if the script stops before it is moved into place.
cleanup() {
  if [[ -n "$TMP_ENV" ]]; then
    rm -f "$TMP_ENV"
  fi
}

# Print the value of the last "KEY=value" line for key $1 in the env file.
read_env_value() {
  sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1 | tr -d '\r'
}

# Replace every FIREBASE_ID_TOKEN= line in the env file with token $1, or append
# one when absent, keeping all other lines and the file mode. A symlinked env
# file is written through, so the link and the file it points to stay intact.
save_token() {
  local token="$1" line found=false target

  # mv would replace a symlink itself, so the temp file is built next to its target.
  target="$(readlink -f "$ENV_FILE")" ||
    fail "Could not resolve $ENV_FILE (readlink -f needs macOS 12.3+ or GNU coreutils)"
  TMP_ENV="$(mktemp "$target.XXXXXX")"
  # cp -p gives the temp file the original mode, which mv then carries over.
  cp -p "$target" "$TMP_ENV"

  # The "|| [[ -n ... ]]" keeps a final line that lacks a trailing newline.
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == FIREBASE_ID_TOKEN=* ]]; then
      printf 'FIREBASE_ID_TOKEN=%s\n' "$token"
      found=true
    else
      printf '%s\n' "$line"
    fi
  done < "$target" > "$TMP_ENV"

  if [[ "$found" == false ]]; then
    printf 'FIREBASE_ID_TOKEN=%s\n' "$token" >> "$TMP_ENV"
  fi

  mv "$TMP_ENV" "$target"
  TMP_ENV=""
}

# Call GET /api/auth/me on CHECK_URL with token $1; exit non-zero unless it is 200.
check_token() {
  local url="${CHECK_URL%/}/api/auth/me" response status curl_status=0

  # The header is read from stdin so the token stays out of the process list.
  response="$(
    printf 'Authorization: Bearer %s\n' "$1" |
      curl -q -s --max-time 30 -H @- -w '\n%{http_code}' "$url" 2>/dev/null
  )" || curl_status=$?

  if ((curl_status != 0)); then
    fail "Could not reach $CHECK_URL (curl exit code $curl_status)"
  fi

  status="${response##*$'\n'}"
  log "GET $url -> HTTP $status"
  log "${response%$'\n'*}"

  [[ "$status" == 200 ]] || exit 1
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

while [[ $# -gt 0 ]]; do
  case "$1" in
    --print)
      PRINT_TOKEN=true
      shift
      ;;
    --check)
      if [[ $# -lt 2 || -z "$2" ]]; then
        usage >&2
        exit 2
      fi
      CHECK_URL="$2"
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

# Keep stdout for the token only: fd 3 is the real stdout, messages go to stderr.
if [[ "$PRINT_TOKEN" == true ]]; then
  exec 3>&1 1>&2
fi

for command in curl jq; do
  if ! command -v "$command" >/dev/null 2>&1; then
    fail "Missing required command: $command"
  fi
done

if [[ ! -f "$ENV_FILE" ]]; then
  fail "Missing $ENV_FILE
Create it from the repository root without overwriting anything:
  (umask 077 && cp -n .env.example .env)
then fill in FIREBASE_WEB_API_KEY, TEST_EMAIL and TEST_PASSWORD
(see docs/firebase-auth-testing.md, section 2)."
fi

if [[ ! -w "$ENV_FILE" ]]; then
  fail "$ENV_FILE is not writable, so a new token could not be saved"
fi

FIREBASE_WEB_API_KEY="$(read_env_value FIREBASE_WEB_API_KEY)"
TEST_EMAIL="$(read_env_value TEST_EMAIL)"
TEST_PASSWORD="$(read_env_value TEST_PASSWORD)"

for name in FIREBASE_WEB_API_KEY TEST_EMAIL TEST_PASSWORD; do
  if [[ -z "${!name}" ]]; then
    fail "$name is missing or empty in $ENV_FILE (see docs/firebase-auth-testing.md, section 2)"
  fi
done

if [[ -n "${FIREBASE_AUTH_EMULATOR_HOST:-}" ]]; then
  AUTH_BASE="http://${FIREBASE_AUTH_EMULATOR_HOST}/identitytoolkit.googleapis.com"
else
  AUTH_BASE="https://identitytoolkit.googleapis.com"
fi

log "Signing in via $AUTH_BASE"

# The password stays out of argv (env for jq, stdin for curl) and curl's stderr is dropped so the URL's API key never prints.
curl_status=0
response="$(
  SIGN_IN_EMAIL="$TEST_EMAIL" SIGN_IN_PASSWORD="$TEST_PASSWORD" \
    jq -n '{email: env.SIGN_IN_EMAIL, password: env.SIGN_IN_PASSWORD, returnSecureToken: true}' |
    curl -q -s --max-time 30 \
      "$AUTH_BASE/v1/accounts:signInWithPassword?key=${FIREBASE_WEB_API_KEY}" \
      -H 'Content-Type: application/json' --data-binary @- \
      -w '\n%{http_code}' 2>/dev/null
)" || curl_status=$?

if ((curl_status != 0)); then
  fail "Could not reach $AUTH_BASE (curl exit code $curl_status)"
fi

status="${response##*$'\n'}"
body="${response%$'\n'*}"

if [[ "$status" != 200 ]]; then
  message="$(printf '%s' "$body" | jq -r '.error.message // empty' 2>/dev/null || true)"
  fail "Firebase sign-in failed (HTTP $status): ${message:-no error message in the response}"
fi

ID_TOKEN="$(printf '%s' "$body" | jq -r '.idToken // empty')" ||
  fail "Could not parse the Firebase sign-in response"
EXPIRES_IN="$(printf '%s' "$body" | jq -r '.expiresIn // empty')" ||
  fail "Could not parse the Firebase sign-in response"

# The token is written into .env and an HTTP header, so anything but a JWT's characters is refused.
if [[ ! "$ID_TOKEN" =~ ^[A-Za-z0-9._-]+$ ]]; then
  fail "Firebase sign-in succeeded but the response contained no valid idToken"
fi

save_token "$ID_TOKEN"
log "Saved FIREBASE_ID_TOKEN to $ENV_FILE (expires in ${EXPIRES_IN:-unknown} seconds)"

if [[ "$PRINT_TOKEN" == true ]]; then
  printf '%s\n' "$ID_TOKEN" >&3
fi

if [[ -n "$CHECK_URL" ]]; then
  check_token "$ID_TOKEN"
fi
