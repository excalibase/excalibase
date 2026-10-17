package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/testca"
)

// A MySQL project's app login is a password over verified TLS; the probe opens
// it as MySQL, not as a Postgres certificate login.
func TestProbeOpensAMySQLRecordAsMySQL(t *testing.T) {
	creds := map[string]string{
		domain.CredentialEngineKey: domain.CredentialEngineMySQL, domain.CredentialTLSKey: domain.CredentialTLSRequired,
		"host": "127.0.0.1", "port": "1", "username": "excalibase_app", "password": "pw", "database": "app",
		domain.CredentialServerCAKey: testca.New(t, "dst MySQL CA").PEM,
	}
	probe := NewVaultDatabaseProbe(&probeVault{creds: creds})
	probe.timeout = time.Second
	err := probe.Probe(context.Background(), "dst")
	if err == nil || !strings.Contains(err.Error(), "query restored database") {
		t.Fatalf("err %v, want the MySQL query to have been tried", err)
	}
	if strings.Contains(err.Error(), "pw") {
		t.Errorf("the password reached the error: %v", err)
	}
}

// EXC-589: a record without the server CA is not opened unverified.
func TestProbeRefusesAMySQLRecordWithoutItsServerCA(t *testing.T) {
	creds := map[string]string{
		domain.CredentialEngineKey: domain.CredentialEngineMySQL, domain.CredentialTLSKey: domain.CredentialTLSRequired,
		"host": "127.0.0.1", "port": "1", "username": "excalibase_app", "password": "pw", "database": "app",
	}
	probe := NewVaultDatabaseProbe(&probeVault{creds: creds})
	if err := probe.Probe(context.Background(), "dst"); err == nil || !strings.Contains(err.Error(), "CA") {
		t.Fatalf("err %v", err)
	}
}
