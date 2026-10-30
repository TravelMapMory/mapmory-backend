#!/usr/bin/env bash
set -euo pipefail

# Run Go integration tests against an isolated PostgreSQL 17 container.
# The script creates its own database and never uses the caller's DATABASE_URL.
#
# Usage:
#   ./scripts/test-postgres-integration.sh
#
# Requirements: Docker, Goose v3.29.0, and Go.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATIONS_DIR="$ROOT_DIR/db/migrations"

TEST_PACKAGES=(
  ./internal/postgres
  ./internal/users
)

CONTAINER_ID=""

section() {
  printf '\n==> %s\n' "$*"
}

info() {
  printf '    %s\n' "$*"
}

cleanup() {
  local status=$?

  if [[ -n "$CONTAINER_ID" ]]; then
    section "Cleaning up"

    if docker rm -f "$CONTAINER_ID" >/dev/null 2>&1; then
      info "Temporary PostgreSQL container removed."
    else
      info "Warning: failed to remove container $CONTAINER_ID."
      status=1
    fi
  fi

  if (( status == 0 )); then
    section "All PostgreSQL integration tests passed"
  else
    section "PostgreSQL integration tests failed (exit $status)"
  fi
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for command in docker goose go; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "Missing required command: $command" >&2
    exit 1
  fi
done

section "PostgreSQL integration tests"

goose -dir "$MIGRATIONS_DIR" validate
info "Migration files validated."

info "Starting isolated PostgreSQL 17..."

CONTAINER_ID="$(
  docker run --rm -d \
    -e POSTGRES_USER=mapmory \
    -e POSTGRES_PASSWORD=local-only \
    -e POSTGRES_DB=mapmory_test \
    -p 127.0.0.1::5432 \
    postgres:17
)"

ready=false

for ((attempt = 1; attempt <= 60; attempt++)); do
  if docker exec "$CONTAINER_ID" \
    pg_isready -U mapmory -d mapmory_test >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done

if [[ "$ready" != true ]]; then
  echo "PostgreSQL did not become ready" >&2
  docker logs "$CONTAINER_ID" >&2
  exit 1
fi

info "PostgreSQL ready."

# Docker assigns a free host port to avoid conflicts with local databases.
port_mapping="$(docker port "$CONTAINER_ID" 5432/tcp)"
host_port="${port_mapping##*:}"

if [[ ! "$host_port" =~ ^[0-9]+$ ]]; then
  echo "Could not determine PostgreSQL host port" >&2
  exit 1
fi

# Construct the connection URL from the container started by this script.
# Never reuse an externally supplied DATABASE_URL for destructive tests.
TEST_URL="postgres://mapmory:local-only@127.0.0.1:${host_port}/mapmory_test?sslmode=disable"

info "Applying database migrations..."
goose -dir "$MIGRATIONS_DIR" postgres "$TEST_URL" up
info "Database ready."

section "Running Go integration tests"

cd "$ROOT_DIR"

MAPMORY_TEST_DATABASE_URL="$TEST_URL" \
  go test -race -tags=integration -count=1 -v "${TEST_PACKAGES[@]}"
