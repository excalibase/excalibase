package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/storage"

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
	// instances lets the probe reach a single host the way its row says; nil reads no row.
	instances storage.InstanceStore
}

// WithInstances makes the probe read each project's row, as the opener does.
func (p *VaultDatabaseProbe) WithInstances(instances storage.InstanceStore) *VaultDatabaseProbe {
	p.instances = instances
	return p
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
	overrides := p.overridesFor(projectID)
	db, err := p.open(creds, overrides)
	if err != nil {
		return err
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

// open connects as the record's engine says: MySQL with its password over
// TLS, Postgres with the role's client certificate.
// overridesFor adds what the project's row states, when the probe can read it.
func (p *VaultDatabaseProbe) overridesFor(projectID string) dsnOverrides {
	if p.instances == nil {
		return p.overrides
	}
	inst, err := p.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		// No row to read: the strict default stands, TLS and the certificate.
		return p.overrides
	}
	return withProjectSSLMode(p.overrides, inst)
}

func (p *VaultDatabaseProbe) open(creds map[string]string, overrides dsnOverrides) (*sql.DB, error) {
	if domain.IsMySQLRecord(creds) {
		port, err := strconv.Atoi(creds["port"])
		if err != nil {
			return nil, fmt.Errorf("read %s credentials: the port is not a number", roleApp)
		}
		inst := &domain.DatabaseInstance{Host: creds["host"], Port: &port}
		config, err := mysqlVerifyConfig(inst, creds["username"], creds["password"], p.overrides, creds[domain.CredentialServerCAKey])
		if err != nil {
			return nil, fmt.Errorf("open restored database as %s: %w", creds["username"], err)
		}
		connector, err := mysql.NewConnector(config)
		if err != nil {
			return nil, fmt.Errorf("open restored database as %s", creds["username"])
		}
		return sql.OpenDB(connector), nil
	}
	dsn, err := buildTenantDSN(creds, overrides)
	if err != nil {
		return nil, err
	}
	db, err := projectdb.OpenPostgres(dsn, projectdb.ProbeIOTimeout)
	if err != nil {
		return nil, fmt.Errorf("open restored database: %w", err)
	}
	return db, nil
}
