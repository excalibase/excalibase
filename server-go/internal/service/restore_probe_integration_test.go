//go:build integration

package service

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// liveDatabaseProbe runs the production *VaultDatabaseProbe against a
// restored Docker container.
//
// The container's host port is assigned by the daemon when the container is
// created, which is part way through the restore under test — so it can only
// be discovered at probe time. That is the one thing this wrapper does: it
// finds the published port, points the SCHEMA_DB_* overrides (the same ones
// the schema and migration paths use for a port-forward) at it, and then
// builds and runs the real probe. The vault read, the DSN, the connection and
// the query are all production code against a live database.
type liveDatabaseProbe struct {
	t     *testing.T
	vault vaultclient.VaultClient
	// corrupt, when set, rewrites the project's filed password before the
	// probe reads it, so the probe meets a database that rejects it.
	corrupt bool
	calls   int
}

func (p *liveDatabaseProbe) Probe(ctx context.Context, projectID string) error {
	p.calls++
	container := fmt.Sprintf("excalibase-%s-postgres", projectID)
	port, err := publishedPostgresPort(ctx, container)
	if err != nil {
		return fmt.Errorf("resolve restored container address: %w", err)
	}
	if p.corrupt {
		if err := p.rewritePassword(projectID); err != nil {
			return err
		}
	}
	p.t.Setenv("SCHEMA_DB_HOST", "127.0.0.1")
	p.t.Setenv("SCHEMA_DB_PORT", port)
	p.t.Setenv("SCHEMA_DB_SSLMODE", "disable")
	return NewVaultDatabaseProbe(p.vault).Probe(ctx, projectID)
}

// rewritePassword files a password the restored database does not hold, so
// the probe's connection is refused by a real postgres rather than by a fake.
func (p *liveDatabaseProbe) rewritePassword(projectID string) error {
	path := vaultCredentialPath(projectID, roleApp)
	creds, err := p.vault.Get(path)
	if err != nil {
		return fmt.Errorf("read filed credentials: %w", err)
	}
	creds["password"] = "not-the-password-this-database-holds"
	return p.vault.Put(path, creds)
}

// publishedPostgresPort asks the daemon which host port the container's 5432
// landed on.
func publishedPostgresPort(ctx context.Context, container string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "port", container, "5432/tcp").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker port %s: %v: %s", container, err, strings.TrimSpace(string(out)))
	}
	// Output is one or more "127.0.0.1:49154" lines.
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
	idx := strings.LastIndex(first, ":")
	if idx < 0 {
		return "", fmt.Errorf("unexpected docker port output %q", first)
	}
	return first[idx+1:], nil
}
