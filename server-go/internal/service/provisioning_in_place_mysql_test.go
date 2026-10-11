package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// A replaced MySQL database brings the backup's passwords; every filed
// platform login is set back to what vault holds, on stdin, never argv.
func TestReapplyCredentialsSetsMySQLPasswordsBackFromVault(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	mock.ExecCommands, mock.ExecStdin = nil, nil

	if err := svc.ReapplyCredentials(context.Background(), inst); err != nil {
		t.Fatalf("ReapplyCredentials: %v", err)
	}
	statements := strings.Join(mock.ExecStdin, "\n")
	for _, role := range []string{provisioner.MySQLUserAdmin, provisioner.MySQLUserApp, provisioner.MySQLUserAuthAdmin, provisioner.MySQLUserWatcher} {
		record, err := svc.vault.Get(vaultCredentialPath(inst.ProjectID, role))
		if err != nil {
			t.Fatalf("vault %s: %v", role, err)
		}
		if !strings.Contains(statements, provisioner.MySQLAlterUserSQL(role, record["password"])) {
			t.Errorf("%s was not set back to its filed password", role)
		}
		for _, cmd := range mock.ExecCommands {
			if strings.Contains(cmd, record["password"]) {
				t.Errorf("a password reached an exec argument: %s", cmd)
			}
		}
	}
	for _, cmd := range mock.ExecCommands {
		if strings.Contains(cmd, "psql") {
			t.Errorf("Postgres ran against a MySQL project: %s", cmd)
		}
	}
}

// No watcher runs for MySQL yet, so there is nothing to stop; the Postgres
// watcher's release in the namespace is never touched.
func TestStopReplicationOfAMySQLProjectIsANoOp(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	mock.Calls = nil
	if err := svc.StopReplication(context.Background(), inst); err != nil {
		t.Fatalf("StopReplication: %v", err)
	}
	for _, call := range mock.Calls {
		if strings.Contains(call, "Helm") {
			t.Errorf("touched a watcher release: %s", call)
		}
	}
}
