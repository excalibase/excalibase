package edgefn

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// PostgresEgressStore keeps each project's outbound allowlist in the platform
// store (edge_function_settings, migration 000014). It is the source of truth
// the runtime is rendered from, so it lives in Postgres in every deployment
// mode — the filesystem function store has no equivalent.
type PostgresEgressStore struct {
	db *sql.DB
}

func NewPostgresEgressStore(db *sql.DB) *PostgresEgressStore {
	return &PostgresEgressStore{db: db}
}

// GetEgressHosts returns the project's allowlist; a project without a row
// reads as empty, which the runtime treats as "no egress".
func (s *PostgresEgressStore) GetEgressHosts(projectID string) ([]string, error) {
	hosts := []string{}
	err := s.db.QueryRow(
		`SELECT egress_allowed_hosts FROM edge_function_settings WHERE project_id = $1`,
		projectID,
	).Scan(pq.Array(&hosts))
	if errors.Is(err, sql.ErrNoRows) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read egress allowlist: %w", err)
	}
	if hosts == nil {
		hosts = []string{}
	}
	return hosts, nil
}

// SetEgressHosts replaces the project's allowlist. nil clears it.
func (s *PostgresEgressStore) SetEgressHosts(projectID string, hosts []string) error {
	if hosts == nil {
		hosts = []string{}
	}
	_, err := s.db.Exec(`
INSERT INTO edge_function_settings (project_id, egress_allowed_hosts, updated_at)
VALUES ($1, $2, $3)
ON CONFLICT (project_id) DO UPDATE SET
    egress_allowed_hosts = EXCLUDED.egress_allowed_hosts,
    updated_at           = EXCLUDED.updated_at`,
		projectID, pq.Array(hosts), time.Now())
	if err != nil {
		return fmt.Errorf("write egress allowlist: %w", err)
	}
	return nil
}

var _ EgressStore = (*PostgresEgressStore)(nil)
