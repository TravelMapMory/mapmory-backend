package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Repository provides persistence operations for MapMory users.
//
// It uses a shared database/sql connection pool. Creating a repository
// does not establish a new database connection.
type Repository struct {
	db *sql.DB
}

// NewRepository creates a user repository backed by db.
//
// The caller retains ownership of the connection pool and is responsible
// for closing it during application shutdown.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// FindOrCreateByAuthSubject resolves a verified Firebase UID to the
// corresponding internal MapMory user ID.
//
// If the UID has no user record, one is created. Concurrent calls for
// the same UID resolve to a single row through the database's unique
// constraint on users.auth_subject.
//
// The caller must supply an authenticated, server-verified UID.
// This method does not verify Firebase tokens or accept client-chosen IDs.
func (r *Repository) FindOrCreateByAuthSubject(
	ctx context.Context,
	authSubject string,
) (uuid.UUID, error) {
	if strings.TrimSpace(authSubject) == "" {
		return uuid.Nil, errors.New("Firebase auth subject is required")
	}

	// Attempt provisioning first. The unique constraint ensures that
	// concurrent requests cannot create separate users for the same UID.
	//
	// DO NOTHING avoids unnecessary updates to existing user records.
	const insertUser = `
		INSERT INTO users (auth_subject)
		VALUES ($1)
		ON CONFLICT (auth_subject) DO NOTHING
		RETURNING id
	`

	var id uuid.UUID

	err := r.db.QueryRowContext(
		ctx,
		insertUser,
		authSubject,
	).Scan(&id)

	switch {
	case err == nil:
		return id, nil

	case !errors.Is(err, sql.ErrNoRows):
		return uuid.Nil, fmt.Errorf("create application user: %w", err)
	}

	// No row was inserted because the auth subject already exists.
	//
	// This is a separate SQL statement on purpose. Under PostgreSQL's
	// default READ COMMITTED isolation, a conflicting concurrent insert
	// can become visible after its transaction commits.
	const findUser = `
		SELECT id
		FROM users
		WHERE auth_subject = $1
	`

	if err := r.db.QueryRowContext(
		ctx,
		findUser,
		authSubject,
	).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("find existing application user: %w", err)
	}

	return id, nil
}
