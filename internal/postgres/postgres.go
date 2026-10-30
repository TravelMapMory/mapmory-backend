package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	maxOpenConnections = 10
	maxIdleConnections = 5
	connectionLifetime = 30 * time.Minute
	connectionIdleTime = 5 * time.Minute
	pingTimeout        = 5 * time.Second
)

// Open creates and verifies a PostgreSQL connection pool.
//
// The caller owns the returned pool and must close it during application
// shutdown. A single pool should be shared between repositories rather
// than created for each request.
//
// The DSN must be supplied by trusted server configuration. In particular,
// do not log it because it may contain database credentials.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("PostgreSQL connection string is required")
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("initialize PostgreSQL: %w", err)
	}

	// sql.Open initializes a pool but normally does not establish
	// a network connection until the first database operation.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}

	// Set conservative limits until production traffic and database
	// connection capacity provide evidence for further tuning.
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxIdleConnections)
	db.SetConnMaxLifetime(connectionLifetime)
	db.SetConnMaxIdleTime(connectionIdleTime)

	// Fail startup promptly if PostgreSQL is unavailable, rather
	// than discovering the problem on the first API request.
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}

	return db, nil
}
