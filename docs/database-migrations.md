# Database migrations

MapMory uses **PostgreSQL 17** for relational data and **Goose v3.29.0** for versioned SQL migrations.

Database migrations are managed independently of the Go application. The backend does not automatically create or migrate database tables when it starts.

## 1. Prerequisites

Install:

- Go 1.26 or newer
- Docker
- Goose v3.29.0

Install Goose using:

```bash
go install github.com/pressly/goose/v3/cmd/goose@v3.29.0
```

Verify the installation:

```bash
goose -version
```

If your shell cannot find `goose`, ensure that `$(go env GOPATH)/bin` is included in your `PATH`.

Goose is installed as a development tool, not as a dependency of the backend application.

## 2. Local PostgreSQL

The [application-user resolution guide](application-user-resolution.md) has the canonical **persistent local container** setup (`mapmory-postgres`, host port 5432, named volume). Use that setup when developing the HTTP API. A named volume preserves users between container restarts.

After starting that container, use the URL configured in your repository-root `.env` for Goose. Unlike the Go server, **Goose does not read `.env`**:

```bash
export DATABASE_URL="$(sed -n 's/^DATABASE_URL=//p' .env | tail -n 1)"
docker exec mapmory-postgres pg_isready -U mapmory -d mapmory
```

The username, password, and disabled TLS setting in the local guide are strictly for a database bound to `127.0.0.1`. For isolated migration lifecycle tests, prefer `./scripts/test-migrations.sh`: it starts a separate temporary PostgreSQL instance and removes it afterward. The two test harnesses never use your normal local `DATABASE_URL`.

## 3. Migration structure

SQL migrations are stored in `db/migrations/` and currently run through version **00009**:

| Version | Main change |
| --- | --- |
| `00001_create_users_and_trips.sql` | Users, owner-scoped trips, UUID/timestamp defaults and ownership indexes |
| `00002_reject_whitespace_only_trip_titles.sql` | Stronger check for common ASCII whitespace-only trip titles |
| `00003_create_upload_batches_and_photos.sql` | Upload batches, photo records, ownership relationships and file constraints |
| `00004_add_photo_processing_fields.sql` | Processing state, errors, timestamps, EXIF/effective coordinates and display-object references |
| `00005_order_photos_by_capture_time.sql` | Capture-order and recent-upload indexes |
| `00006_add_photo_place_labels.sql` | Country/city/place labels, provider attribution and confirmation status |
| `00007_create_shares.sql` | Link/public shares, selected photos, stored coarse coordinates and database constraints |
| `00008_create_share_reports.sql` | Public-share reports and unresolved-report index |
| `00009_add_photo_label_attempts.sql` | Retry counter and bounds for automatic place labels |

Migration 00007 uses composite foreign keys to ensure that a selected photo belongs to the share's trip. The share token is unique and share/photo relationships are indexed. The HTTP layer separately checks that selected photos are processed.

Migrations already applied to shared databases must not be changed or renumbered. Add a new migration for future changes.

### Creating a migration

From the repository root:

```bash
goose -dir db/migrations -s create descriptive_name sql
```

Review the generated file and define the `-- +goose Up` and `-- +goose Down` sections.

Migrations should contain explicit schema changes and database constraints. Avoid application-specific business workflows inside migration files.

**Once a migration has been applied to a shared database, do not modify or renumber it.** Create a new migration for subsequent changes.

## 4. Running migrations

All commands below are intended to be run from the backend repository root.

### Validate migration files

```bash
goose -dir db/migrations validate
```

This validates migration structure without connecting to PostgreSQL.

For verbose output:

```bash
goose -dir db/migrations -v validate
```

### Apply pending migrations

```bash
goose -dir db/migrations postgres "$DATABASE_URL" up
```

Goose tracks applied versions in its `goose_db_version` table. Repeating `up` when the schema is current does not reapply completed migrations.

### Inspect status

```bash
goose -dir db/migrations postgres "$DATABASE_URL" status
```

### Inspect the schema

```bash
docker exec mapmory-postgres \
  psql -U mapmory -d mapmory -c '\dt'

docker exec mapmory-postgres \
  psql -U mapmory -d mapmory -c '\d users'

docker exec mapmory-postgres \
  psql -U mapmory -d mapmory -c '\d trips'
```

### Roll back a migration

**Warning: rollback may permanently delete tables or data. Verify the database connection before running it.**

On an explicitly disposable database only:

```bash
goose -dir db/migrations postgres "$DATABASE_URL" down
```

`down` reverts the latest applied migration, currently `00009` on a fully migrated database. Repeating it rolls back earlier migrations; later steps can drop sharing, photo, trip and user tables and data. Never run this sequence against a shared database.

Do not use `down`, `reset`, or `redo` as routine production recovery commands. Production schema changes require a deliberate migration and recovery plan.

## 5. Automated migration testing

The repository provides two complementary testing tools:

| File | Purpose |
|---|---|
| `db/tests/schema_constraints.sql` | Verify the constraints and defaults of an already-migrated PostgreSQL database |
| `scripts/test-migrations.sh` | Test the complete migration lifecycle against a fresh, isolated PostgreSQL container |

### Running schema constraint tests

The SQL test file checks:

- Database-generated UUIDs and timestamps.
- Unique and required Firebase authentication subjects.
- Valid trip ownership and rejection of nonexistent owners.
- Required, nonblank trip titles.
- Default trip notes.
- Restricted deletion of users who own trips.
- Photo object uniqueness and relevant photo processing constraints (in the current SQL constraint script).

First, ensure the local PostgreSQL container is running and migrations have been applied:

```bash
goose -dir db/migrations postgres "$DATABASE_URL" up
```

Then run the SQL tests from the repository root:

```bash
docker exec -i mapmory-postgres \
  psql -X -v ON_ERROR_STOP=1 \
  -U mapmory -d mapmory \
  < db/tests/schema_constraints.sql
```

Expected output:

```text
BEGIN
NOTICE:  All schema constraint tests passed
DO
ROLLBACK
```

The tests run inside a transaction and roll back their changes, so they can be repeated without accumulating test users or trips.

The command uses `docker exec` to run `psql` inside the existing PostgreSQL container. It does not require a local PostgreSQL client installation.

Alternatively, if `psql` is installed locally and `DATABASE_URL` points to the intended database:

```bash
psql "$DATABASE_URL" \
  -X -v ON_ERROR_STOP=1 \
  -f db/tests/schema_constraints.sql
```

**Use these commands only against a local or disposable test database.** Although the SQL test rolls back its own changes, it performs inserts, constraint checks, and attempted deletions during execution.

### Running the complete migration lifecycle tests

To validate migrations from a fresh database, run:

```bash
./scripts/test-migrations.sh
```

The script:

1. Validates the Goose migration files.
2. Starts its own temporary PostgreSQL 17 container.
3. Applies the migrations.
4. Executes `db/tests/schema_constraints.sql`.
5. Repeats `goose up` to verify that no migrations remain pending.
6. Rolls back migrations 00009 through 00001 in order and verifies that application tables were removed.
7. Reapplies all nine migrations and reruns the SQL tests.
8. Removes the temporary container, including when a command fails.

Unlike the standalone SQL test, the lifecycle script creates and manages its own database. It does not use the caller's `DATABASE_URL`, so it cannot accidentally roll back the normal local development database through that variable.

The lifecycle script currently exercises all nine migrations. When adding another migration, update its rollback sequence and assertions.

### CI

The `database-migrations` GitHub Actions job runs the same script using Goose v3.29.0.

Database migration testing is independent of ordinary Go unit tests and requires neither Firebase credentials nor a Cloud SQL connection.

## 6. Conventions and operational safety

- Use **plain SQL migrations** under `db/migrations`.
- Keep migration versions sequential and committed to Git.
- Pin Goose to **v3.29.0** for local development and CI.
- Prefer database constraints for relational invariants, including unique keys, required fields, and foreign keys.
- Add indexes based on expected query patterns rather than indexing every column.
- Keep schema migration separate from application startup and Docker image builds.
- Do not modify migrations already applied to shared environments.
- Test destructive migration operations only on isolated, disposable databases.
- Never commit actual database credentials or use local testing commands against production.

The schema is independent of application repositories; PostgreSQL-backed users, trips, uploads, photos and shares are implemented in their corresponding `internal/` packages.

## 7. Related work

The migrated schema supports authenticated application users, owner-scoped trips, upload processing, photo metadata/place labels and fixed link/public shares.

Run `./scripts/test-postgres-integration.sh` to exercise these repositories against a disposable PostgreSQL instance with Go's race detector. Local migration and integration results do **not** establish that migrations have been executed on the production database or that the deployed VM is configured for database mode. See [Deployment](../deploy/README.md).
