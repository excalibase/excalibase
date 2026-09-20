package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// probeVault serves one credential set, or refuses.
type probeVault struct {
	creds map[string]string
	err   error
	paths []string
}

func (v *probeVault) Get(path string) (map[string]string, error) {
	v.paths = append(v.paths, path)
	if v.err != nil {
		return nil, v.err
	}
	return v.creds, nil
}

func (v *probeVault) Put(string, map[string]string) error { return nil }
func (v *probeVault) Delete(string) error                 { return nil }
func (v *probeVault) DeletePrefix(string) (int, error)    { return 0, nil }
func (v *probeVault) List(string) ([]string, error)       { return nil, nil }
func (v *probeVault) Sealed() bool                        { return false }
func (v *probeVault) GetPublicKey() (string, error)       { return "", nil }

func TestProbeReadsTheProjectsAppCredentials(t *testing.T) {
	vault := &probeVault{creds: closedPortCreds()}
	probe := NewVaultDatabaseProbe(vault)
	probe.timeout = time.Second

	if err := probe.Probe(context.Background(), "dst"); err == nil {
		t.Fatal("a database that refuses connections must fail the probe")
	}
	want := "projects/dst/credentials/" + roleApp
	if len(vault.paths) != 1 || vault.paths[0] != want {
		t.Errorf("credential path: got %v, want [%s]", vault.paths, want)
	}
}

func TestProbeFailsWhenCredentialsCannotBeRead(t *testing.T) {
	sealed := errors.New("vault is sealed")
	probe := NewVaultDatabaseProbe(&probeVault{err: sealed})

	err := probe.Probe(context.Background(), "dst")
	if !errors.Is(err, sealed) {
		t.Fatalf("err: got %v, want the vault error", err)
	}
}

func TestProbeReportsAQueryThatDoesNotAnswer(t *testing.T) {
	probe := NewVaultDatabaseProbe(&probeVault{creds: closedPortCreds()})
	probe.timeout = time.Second

	err := probe.Probe(context.Background(), "dst")
	if err == nil || !strings.Contains(err.Error(), "query restored database") {
		t.Fatalf("err: got %v, want a query failure", err)
	}
}

// closedPortCreds point at a port nothing listens on, so the probe fails the
// way an unrecovered database would — immediately and without a server.
func closedPortCreds() map[string]string {
	return map[string]string{
		"host": "127.0.0.1", "port": "1",
		"username": "excalibase_app", "password": "pw", "database": "app",
	}
}
