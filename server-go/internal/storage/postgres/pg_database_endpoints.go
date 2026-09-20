package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// dbEndpointPortLockSpace namespaces the advisory lock the public-port
// allocator runs under (EXC-410). Postgres keeps two-integer advisory locks
// in a key space of their own, so this cannot collide with the single-bigint
// locks the schedulers lead on, nor with the per-organisation project-slot
// lock in its own space.
const dbEndpointPortLockSpace = 410

// dbEndpointPortLockKey is the second half of the lock key. The whole port
// window is one resource — a candidate is chosen by looking at every port in
// it — so every allocation serializes on the same value.
const dbEndpointPortLockKey = 0

// GetDatabaseEndpoint returns the project's public endpoint setting. See
// storage.DatabaseEndpointStore.
func (s *Store) GetDatabaseEndpoint(ctx context.Context, projectID string) (domain.DBEndpoint, error) {
	return readDBEndpoint(ctx, s.db, projectID, domain.DBEndpointRolePostgres)
}

// GetDatabaseEndpointForRole returns one of the project's holdings. See
// storage.DatabaseEndpointStore.
func (s *Store) GetDatabaseEndpointForRole(
	ctx context.Context, projectID string, role domain.DBEndpointRole,
) (domain.DBEndpoint, error) {
	if !role.Valid() {
		return domain.DBEndpoint{}, fmt.Errorf("read database endpoint: unknown endpoint role %q", role)
	}
	return readDBEndpoint(ctx, s.db, projectID, role)
}

// AllocateDatabaseEndpointPort takes a port for the project. See
// storage.DatabaseEndpointStore.
//
// Counting free ports and then writing one would be a read-then-write: two
// concurrent allocations at READ COMMITTED would both see the same port free
// and one would lose on the unique index, or worse, both would be told a
// different truth about exhaustion. So the transaction takes an advisory lock
// on the port space first and holds it to commit, exactly as the
// per-organisation project slot does. The unique index on port remains as the
// backstop.
//
// The candidate is chosen at random from what is free rather than as the
// lowest available number: sequential ports would let anyone holding two of
// them read off how many tenants the platform has and in what order they
// signed up.
func (s *Store) AllocateDatabaseEndpointPort(ctx context.Context, projectID string, role domain.DBEndpointRole, window domain.PortRange, quarantine time.Duration) (domain.DBEndpoint, error) {
	if !role.Valid() {
		return domain.DBEndpoint{}, fmt.Errorf("allocate database endpoint port: unknown endpoint role %q", role)
	}
	if err := window.Validate(); err != nil {
		return domain.DBEndpoint{}, fmt.Errorf("allocate database endpoint port: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.DBEndpoint{}, fmt.Errorf("begin database endpoint allocation: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	if err := lockDBEndpointPorts(ctx, tx); err != nil {
		return domain.DBEndpoint{}, err
	}
	held, err := readDBEndpoint(ctx, tx, projectID, role)
	if err != nil {
		return domain.DBEndpoint{}, err
	}
	// A project that already holds a port for this protocol keeps it, so a
	// retried enable and a resume both come back on the number its customers
	// have saved.
	if held.Port > 0 {
		if err := tx.Commit(); err != nil {
			return domain.DBEndpoint{}, fmt.Errorf("commit database endpoint allocation: %w", err)
		}
		return held, nil
	}
	port, err := pickFreeDBEndpointPort(ctx, tx, window, time.Now().Add(-quarantine))
	if err != nil {
		return domain.DBEndpoint{}, err
	}
	endpoint, err := writeDBEndpointPort(ctx, tx, projectID, role, port)
	if err != nil {
		return domain.DBEndpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.DBEndpoint{}, fmt.Errorf("commit database endpoint allocation: %w", err)
	}
	return endpoint, nil
}

// SetDatabaseEndpointPublic records whether the project's Service exists. See
// storage.DatabaseEndpointStore.
func (s *Store) SetDatabaseEndpointPublic(ctx context.Context, projectID string, public bool) (domain.DBEndpoint, error) {
	return s.writeDBEndpointFlag(ctx, projectID, setPublicSQL, public, "public")
}

// SetDatabaseEndpointRequireTLS records the project's TLS choice. See
// storage.DatabaseEndpointStore.
func (s *Store) SetDatabaseEndpointRequireTLS(ctx context.Context, projectID string, requireTLS bool) (domain.DBEndpoint, error) {
	return s.writeDBEndpointFlag(ctx, projectID, setRequireTLSSQL, requireTLS, "require_tls")
}

// Each settable column has its own statement. A single helper taking the
// column name would put an identifier into the statement text, which is the
// shape an injection takes even when today's callers only pass constants.
const setPublicSQL = `
INSERT INTO database_endpoints (project_id, role, public, updated_at)
VALUES ($1, 'postgres', $2, NOW())
ON CONFLICT (project_id, role) DO UPDATE SET public = EXCLUDED.public, updated_at = NOW()
RETURNING project_id, role, port, public, require_tls`

const setRequireTLSSQL = `
INSERT INTO database_endpoints (project_id, role, require_tls, updated_at)
VALUES ($1, 'postgres', $2, NOW())
ON CONFLICT (project_id, role) DO UPDATE SET require_tls = EXCLUDED.require_tls, updated_at = NOW()
RETURNING project_id, role, port, public, require_tls`

// writeDBEndpointFlag runs one of the statements above, creating the row at
// its defaults when the project has never touched the setting.
func (s *Store) writeDBEndpointFlag(ctx context.Context, projectID, query string, value bool, name string) (domain.DBEndpoint, error) {
	endpoint, err := scanDBEndpoint(s.db.QueryRowContext(ctx, query, projectID, value))
	if err != nil {
		return domain.DBEndpoint{}, fmt.Errorf("write database endpoint %s: %w", name, err)
	}
	return endpoint, nil
}

// ReleaseDatabaseEndpointPort frees the project's port into quarantine. See
// storage.DatabaseEndpointStore.
//
// The port goes into quarantine in the same transaction that clears it from
// the project, so there is no instant in which it is neither held nor held
// back. Releasing a project that holds no port is a no-op, which is what a
// retried teardown needs.
func (s *Store) ReleaseDatabaseEndpointPort(ctx context.Context, projectID string, role domain.DBEndpointRole, releasedAt time.Time) error {
	if !role.Valid() {
		return fmt.Errorf("release database endpoint port: unknown endpoint role %q", role)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin database endpoint release: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	if err := lockDBEndpointPorts(ctx, tx); err != nil {
		return err
	}
	held, err := readDBEndpoint(ctx, tx, projectID, role)
	if err != nil {
		return err
	}
	if held.Port > 0 {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO database_endpoint_port_quarantine (port, released_at, released_project_id)
VALUES ($1, $2, $3)
ON CONFLICT (port) DO UPDATE SET
    released_at         = EXCLUDED.released_at,
    released_project_id = EXCLUDED.released_project_id`,
			held.Port, releasedAt, projectID); err != nil {
			return fmt.Errorf("quarantine database endpoint port: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE database_endpoints SET port = NULL, public = FALSE, updated_at = NOW()
WHERE project_id = $1 AND role = $2`, projectID, string(role)); err != nil {
		return fmt.Errorf("clear database endpoint port: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit database endpoint release: %w", err)
	}
	return nil
}

// DeleteDatabaseEndpoint removes the project's row. See
// storage.DatabaseEndpointStore. The quarantine entry is deliberately left
// behind: it outlives the project whose clients are still dialling its port.
func (s *Store) DeleteDatabaseEndpoint(ctx context.Context, projectID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM database_endpoints WHERE project_id = $1`, projectID); err != nil {
		return fmt.Errorf("delete database endpoint: %w", err)
	}
	return nil
}

// lockDBEndpointPorts serializes every allocation and release on the port
// space. The lock releases at end of transaction, on rollback too.
func lockDBEndpointPorts(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`,
		dbEndpointPortLockSpace, dbEndpointPortLockKey); err != nil {
		return fmt.Errorf("lock database endpoint ports: %w", err)
	}
	return nil
}

// pickFreeDBEndpointPort returns a port no project holds and no quarantine
// still covers, chosen at random inside the window. Exhaustion is a refusal,
// never a wrap-around onto a port whose quarantine has not expired.
func pickFreeDBEndpointPort(ctx context.Context, tx *sql.Tx, window domain.PortRange, quarantineCutoff time.Time) (int, error) {
	var port int
	err := tx.QueryRowContext(ctx, `
SELECT candidate FROM generate_series($1::int, $2::int) AS candidate
WHERE NOT EXISTS (SELECT 1 FROM database_endpoints WHERE port = candidate)
  AND NOT EXISTS (
      SELECT 1 FROM database_endpoint_port_quarantine
      WHERE port = candidate AND released_at > $3)
ORDER BY random()
LIMIT 1`, window.Min, window.Max, quarantineCutoff).Scan(&port)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: every port between %d and %d is held or in quarantine",
			storage.ErrDBEndpointPortsExhausted, window.Min, window.Max)
	}
	if err != nil {
		return 0, fmt.Errorf("choose database endpoint port: %w", err)
	}
	return port, nil
}

// writeDBEndpointPort stamps the chosen port on the project, creating the row
// at its defaults when the project has never touched the setting. The
// endpoint stays unpublished: only an observed Service flips that.
func writeDBEndpointPort(ctx context.Context, tx *sql.Tx, projectID string, role domain.DBEndpointRole, port int) (domain.DBEndpoint, error) {
	endpoint, err := scanDBEndpoint(tx.QueryRowContext(ctx, `
INSERT INTO database_endpoints (project_id, role, port, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (project_id, role) DO UPDATE SET port = EXCLUDED.port, updated_at = NOW()
RETURNING project_id, role, port, public, require_tls`, projectID, string(role), port))
	if err != nil {
		return domain.DBEndpoint{}, fmt.Errorf("write database endpoint port: %w", err)
	}
	return endpoint, nil
}

// dbEndpointQuerier is the part of *sql.DB and *sql.Tx these reads use, so a
// read can run either inside the allocating transaction or on its own.
type dbEndpointQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// readDBEndpoint returns the project's row, or the default — off, no port,
// TLS required — when it has none.
func readDBEndpoint(ctx context.Context, q dbEndpointQuerier, projectID string, role domain.DBEndpointRole) (domain.DBEndpoint, error) {
	endpoint, err := scanDBEndpoint(q.QueryRowContext(ctx,
		`SELECT project_id, role, port, public, require_tls FROM database_endpoints WHERE project_id = $1 AND role = $2`,
		projectID, string(role)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DefaultDBEndpointForRole(projectID, role), nil
	}
	if err != nil {
		return domain.DBEndpoint{}, fmt.Errorf("read database endpoint: %w", err)
	}
	return endpoint, nil
}

// scanDBEndpoint maps a row onto the domain type. A NULL port reads as 0 —
// "holds none" — which is what domain.DBEndpoint.IsPublic asks about.
func scanDBEndpoint(row *sql.Row) (domain.DBEndpoint, error) {
	var (
		endpoint domain.DBEndpoint
		port     sql.NullInt64
	)
	if err := row.Scan(&endpoint.ProjectID, &endpoint.Role, &port, &endpoint.PublicEnabled, &endpoint.RequireTLS); err != nil {
		return domain.DBEndpoint{}, err
	}
	if port.Valid {
		endpoint.Port = int(port.Int64)
	}
	return endpoint, nil
}

var _ storage.DatabaseEndpointStore = (*Store)(nil)
