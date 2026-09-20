package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// probeSQL is the cheapest statement that proves a connection reached a
// serving postgres rather than a listening socket.
const probeSQL = "SELECT 1"

// defaultProbeTimeout bounds a single probe. A recovered database that has
// been observed ready either answers promptly or is not serving.
const defaultProbeTimeout = 10 * time.Second

// VaultDatabaseProbe proves a project's database is usable by opening it the
// way the rest of the platform does — the excalibase_app credentials filed in
// vault under the project's id — and running a trivial query. Anything less
// would confirm the operator's view of the cluster, not the database.
type VaultDatabaseProbe struct {
	vault     vaultclient.VaultClient
	overrides dsnOverrides
	timeout   time.Duration
}

// NewVaultDatabaseProbe wires the probe against the platform's vault. It
// honours the same SCHEMA_DB_* overrides the schema and migration paths use,
// so a local port-forward is probed rather than an in-cluster service name.
func NewVaultDatabaseProbe(vault vaultclient.VaultClient) *VaultDatabaseProbe {
	return &VaultDatabaseProbe{
		vault: vault,
		overrides: dsnOverrides{
			host:    os.Getenv("SCHEMA_DB_HOST"),
			port:    os.Getenv("SCHEMA_DB_PORT"),
			sslmode: os.Getenv("SCHEMA_DB_SSLMODE"),
		},
		timeout: defaultProbeTimeout,
	}
}

func (p *VaultDatabaseProbe) Probe(ctx context.Context, projectID string) error {
	creds, err := p.vault.Get(fmt.Sprintf("projects/%s/credentials/%s", projectID, roleApp))
	if err != nil {
		return fmt.Errorf("read %s credentials: %w", roleApp, err)
	}
	db, err := sql.Open("postgres", buildTenantDSN(creds, p.overrides))
	if err != nil {
		return fmt.Errorf("open restored database: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	var one int
	if err := db.QueryRowContext(ctx, probeSQL).Scan(&one); err != nil {
		return fmt.Errorf("query restored database: %w", err)
	}
	return nil
}
