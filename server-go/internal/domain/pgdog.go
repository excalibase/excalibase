package domain

import "time"

// PgDogDatabase represents a database entry in PgDog's config table.
type PgDogDatabase struct {
	ID           int64     `json:"id,omitempty"`
	Name         string    `json:"name"`         // logical db name (e.g. "project_alpha")
	Host         string    `json:"host"`         // upstream Postgres host
	Port         int       `json:"port"`
	DatabaseName string    `json:"databaseName"` // actual PG database name
	Role         string    `json:"role"`         // "primary" | "replica" | "auto"
	Shard        int       `json:"shard"`
	PoolSize     *int      `json:"poolSize,omitempty"`
	ReadOnly     bool      `json:"readOnly"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"createdAt"`
}

// PgDogUser represents a user entry in PgDog's config table.
type PgDogUser struct {
	ID       int64  `json:"id,omitempty"`
	Name     string `json:"name"`     // Postgres username
	Database string `json:"database"` // matches PgDogDatabase.Name
	Password string `json:"password"`
	PoolSize *int   `json:"poolSize,omitempty"`
	Active   bool   `json:"active"`
}
