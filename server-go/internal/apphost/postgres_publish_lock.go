package apphost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
)

// publishLockSpace keeps one project's image publishes apart from other advisory locks.
const publishLockSpace = 540

// PostgresPublishLock serializes a project's image publishes across replicas
// (EXC-540): the quota is measured, the image pushed and its room made under
// one lock. The lock lives in a transaction that does nothing else, so a
// replica that dies drops it with its connection.
type PostgresPublishLock struct {
	db *sql.DB
}

func NewPostgresPublishLock(db *sql.DB) *PostgresPublishLock {
	return &PostgresPublishLock{db: db}
}

// Lock waits for the project's lock; the returned function releases it.
func (l *PostgresPublishLock) Lock(ctx context.Context, projectID string) (func(), error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin the publish lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, publishLockSpace, projectID); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("take the publish lock: %w", err)
	}
	return func() {
		// A rollback that fails leaves the connection broken; Postgres drops the lock with it.
		// ErrTxDone is a cancelled context that already rolled it back.
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			log.Printf("publish lock %s: release: %v", projectID, err)
		}
	}, nil
}
