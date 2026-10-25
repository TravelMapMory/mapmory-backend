package main

import (
	"database/sql"

	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/TravelMapMory/mapmory-backend/internal/users"
)

// databaseDependencies contains repositories backed by a single, shared pool.
// The server bootstrap owns the pool; repositories must never close it.
type databaseDependencies struct {
	users *users.Repository
	trips trips.Repository
}

func newDatabaseDependencies(db *sql.DB) databaseDependencies {
	return databaseDependencies{
		users: users.NewRepository(db),
		trips: trips.NewPostgresRepository(db),
	}
}
