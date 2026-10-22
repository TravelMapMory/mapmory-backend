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

For development, start a disposable PostgreSQL 17 container:

```bash
docker run --rm \
  --name mapmory-postgres \
  -e POSTGRES_USER=mapmory \
  -e POSTGRES_PASSWORD=local-only \
  -e POSTGRES_DB=mapmory \
  -p 127.0.0.1:5433:5432 \
  -d postgres:17
```

The container exposes PostgreSQL on port `5433` of the local machine, while PostgreSQL listens on port `5432` inside the container.

Check that it is ready:

```bash
docker exec mapmory-postgres \
  pg_isready -U mapmory -d mapmory
```

Configure the connection URL in the current terminal:

```bash
export DATABASE_URL='postgres://mapmory:local-only@127.0.0.1:5433/mapmory?sslmode=disable'
```

Use the published host port shown by Docker if it differs:

```bash
docker port mapmory-postgres 5432
```

The password and disabled TLS configuration above are intended only for disposable local development.

**Data persistence:** This container does not use a persistent Docker volume. Removing it discards its database contents. Stop and remove it with:

```bash
docker stop mapmory-postgres
```

Do not use this configuration for production or shared databases.

## 3. Migration structure

SQL migrations are stored in:

```text
db/
└── migrations/
    └── 00001_create_users_and_trips.sql
```

MapMory uses sequential migration numbers and Goose's SQL annotations.

The initial migration creates:

- `users`: internal UUID, unique Firebase authentication subject, display name, and creation timestamp.
- `trips`: internal UUID, owner foreign key, title, notes, and creation timestamp.

It also creates an index supporting owner-scoped trip listing.

The `photos` and `upload_batches` tables are intentionally deferred.

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

`down` reverts one applied migration. For the initial schema, that drops the `trips` and `users` tables, including their contents.

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
6. Rolls back the initial migration and checks that the application tables were removed.
7. Reapplies the migration and reruns the SQL tests.
8. Removes the temporary container, including when a command fails.

Unlike the standalone SQL test, the lifecycle script creates and manages its own database. It does not use the caller's `DATABASE_URL`, so it cannot accidentally roll back the normal local development database through that variable.

The lifecycle script currently assumes a single initial migration. Its rollback assertions must be revisited when additional migrations are introduced.

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

The initial schema deliberately does not introduce an ORM or repository implementation.

## 7. Related work

This database foundation supports:

- **Backend #32:** Resolving verified Firebase UIDs into internal user records.
- **Backend #17:** Persisting trips using PostgreSQL.
- **Backend #9:** Enforcing owner-only access to trips.

Application-level database access, ownership enforcement, photo/upload schema, Cloud SQL provisioning, and production migration execution are outside the scope of backend #7.
