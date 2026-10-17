package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/testca"
)

// EXC-589: cert-manager renews a MySQL project's server certificate on its
// own, and mysqld reads it again only when told. The sweep tells every
// running instance, which leaves open sessions alone.
func TestMaintainMySQLTLS_ReloadsTheRunningServer(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	before := len(mock.ExecCommands)
	report := svc.MaintainMySQLTLS(context.Background())
	if len(report.Failed) != 0 {
		t.Fatalf("failed: %v", report.Failed)
	}
	reloads := 0
	for _, cmd := range mock.ExecCommands[before:] {
		if strings.Contains(cmd, "ALTER INSTANCE RELOAD TLS") {
			reloads++
		}
	}
	if reloads != 1 {
		t.Errorf("%d reloads of %s's one instance: %v", reloads, inst.ProjectID, mock.ExecCommands[before:])
	}
	if report.Renewed != 0 {
		t.Errorf("records refiled although the CA did not change: %d", report.Renewed)
	}
}

// When the CA changes (cert-manager re-signed it, or a restore brought a new
// one), every filed login is refiled with it, passwords untouched, so the
// engine, functions and apps verify against what the server presents.
func TestMaintainMySQLTLS_RefilesAChangedCA(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	vault := svc.vault.(*fakeVault)
	passwords := map[string]string{}
	for _, role := range []string{roleAdmin, roleApp, roleAuthAdmin, roleWatcher} {
		record, _ := vault.Get(vaultCredentialPath(inst.ProjectID, role))
		passwords[role] = record["password"]
	}
	renewed := testca.New(t, "renewed").PEM
	mock.Secrets[inst.Namespace+"/"+k8s.MySQLCASecretName(inst.ProjectID)] = map[string][]byte{"tls.crt": []byte(renewed)}

	report := svc.MaintainMySQLTLS(context.Background())
	if len(report.Failed) != 0 || report.Renewed != 1 {
		t.Fatalf("report %+v", report)
	}
	for role, password := range passwords {
		record, _ := vault.Get(vaultCredentialPath(inst.ProjectID, role))
		if record["sslrootcert"] != renewed || record["password"] != password {
			t.Errorf("%s: CA refiled=%v password kept=%v", role, record["sslrootcert"] == renewed, record["password"] == password)
		}
	}
}

// A project another operation holds (a rotation writing its records) is left
// for the next pass rather than raced.
func TestMaintainMySQLTLS_SkipsAProjectInUse(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	release, claimed, err := svc.claimer().Claim(context.Background(), inst.ProjectID, OperationRotation)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	defer release()
	mock.Secrets[inst.Namespace+"/"+k8s.MySQLCASecretName(inst.ProjectID)] = map[string][]byte{"tls.crt": []byte(testca.New(t, "renewed").PEM)}
	report := svc.MaintainMySQLTLS(context.Background())
	if report.Renewed != 0 || len(report.Failed) != 0 {
		t.Fatalf("report %+v", report)
	}
}

// A Postgres project is not this sweep's.
func TestMaintainMySQLTLS_LeavesPostgresAlone(t *testing.T) {
	svc, _, mock := setupMySQLTest(t)
	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: "pg-only", OrgID: "org1", DBType: domain.PostgreSQL,
	})
	if err != nil || resp.Status != "ACTIVE" {
		t.Fatalf("Provision: %v %+v", err, resp)
	}
	before := len(mock.ExecCommands)
	if report := svc.MaintainMySQLTLS(context.Background()); report.Renewed != 0 || len(report.Failed) != 0 {
		t.Fatalf("report %+v", report)
	}
	for _, cmd := range mock.ExecCommands[before:] {
		if strings.Contains(cmd, "RELOAD TLS") {
			t.Errorf("reloaded a Postgres project: %s", cmd)
		}
	}
}

// An in-place restore reapplies the filed logins and refiles the CA with them.
func TestReapplyMySQLCredentialsRefilesTheServerCA(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	restored := testca.New(t, "restored").PEM
	mock.Secrets[inst.Namespace+"/"+k8s.MySQLCASecretName(inst.ProjectID)] = map[string][]byte{"tls.crt": []byte(restored)}
	if err := svc.ReapplyCredentials(context.Background(), inst); err != nil {
		t.Fatalf("ReapplyCredentials: %v", err)
	}
	record, _ := svc.vault.Get(vaultCredentialPath(inst.ProjectID, roleApp))
	if record["sslrootcert"] != restored {
		t.Error("the app login still carries the old CA")
	}
}
