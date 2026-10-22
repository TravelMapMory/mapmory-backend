#!/usr/bin/env bash
set -euo pipefail

# Runs destructive migration lifecycle tests against a temporary
# PostgreSQL container owned by this script. Never uses DATABASE_URL
# supplied by the caller.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATIONS_DIR="$ROOT_DIR/db/migrations"
TEST_FILE="$ROOT_DIR/db/tests/schema_constraints.sql"

CONTAINER=""
TEST_DATABASE_URL=""

log() {
  printf '\n==> %s\n' "$*"
}

cleanup() {
  if [[ -n "$CONTAINER" ]]; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  fi
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for command in docker goose; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "Missing required command: $command" >&2
    exit 1
  fi
done

if [[ ! -f "$TEST_FILE" ]]; then
  echo "Missing constraint tests: $TEST_FILE" >&2
  exit 1
fi

# Validate files before starting PostgreSQL.
log "Validating Goose migrations"
goose -dir "$MIGRATIONS_DIR" validate

log "Starting isolated PostgreSQL 17"
CONTAINER="mapmory-migration-test-$$"

docker run --rm -d \
  --name "$CONTAINER" \
  -e POSTGRES_USER=mapmory \
  -e POSTGRES_PASSWORD=local-only \
  -e POSTGRES_DB=mapmory_test \
  -p 127.0.0.1::5432 \
  postgres:17 >/dev/null

log "Waiting for PostgreSQL"
ready=false

for _ in {1..60}; do
  if docker exec "$CONTAINER" \
    pg_isready -U mapmory -d mapmory_test >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done

if [[ "$ready" != true ]]; then
  echo "PostgreSQL did not become ready" >&2
  docker logs "$CONTAINER" >&2
  exit 1
fi

# Docker allocates a free host port. Do not reuse the caller's
# DATABASE_URL: lifecycle tests must target only this container.
port_mapping="$(docker port "$CONTAINER" 5432/tcp)"
host_port="${port_mapping##*:}"

if [[ ! "$host_port" =~ ^[0-9]+$ ]]; then
  echo "Could not determine PostgreSQL host port" >&2
  exit 1
fi

TEST_DATABASE_URL="postgres://mapmory:local-only@127.0.0.1:${host_port}/mapmory_test?sslmode=disable"

migrate() {
  goose -dir "$MIGRATIONS_DIR" postgres "$TEST_DATABASE_URL" "$1"
}

run_constraints() {
  docker exec -i "$CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 \
    -U mapmory -d mapmory_test \
    < "$TEST_FILE"
}

# Database migrations
log "Applying migrations"
migrate up

log "Running schema constraint tests"
run_constraints

log "Checking repeated migration application"
migrate up

log "Rolling back the initial migration"
migrate down

log "Verifying that application tables were removed"
remaining_tables="$(
  docker exec "$CONTAINER" \
    psql -X -A -t -v ON_ERROR_STOP=1 \
    -U mapmory -d mapmory_test \
    -c "SELECT count(*) FROM pg_tables
        WHERE schemaname = 'public'
          AND tablename IN ('users', 'trips');"
)"

if [[ "$remaining_tables" != "0" ]]; then
  echo "Rollback left users or trips tables behind" >&2
  exit 1
fi

log "Reapplying migrations"
migrate up

log "Rerunning schema constraint tests"
run_constraints

log "Final migration status"
migrate status

log "All migration lifecycle tests passed"
