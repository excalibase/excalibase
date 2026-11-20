package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// EXC-765: apps and functions connect with a login of their own, filed beside
// the platform's, instead of the owner's.

func TestRegistrationFilesTheRuntimeLoginWhileItsFeatureIsOn(t *testing.T) {
	h := newRegistrationHarness(t)
	h.svc.SetFeatures(features.NewStatic(features.AppRuntimeLogin))
	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{ResetRolePasswords: true}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	record, err := h.vault.Get(vaultCredentialPath(testRegProject, roleRuntime))
	if err != nil || record["username"] != roleRuntime || len(record["password"]) < 32 {
		t.Fatalf("runtime record %v, %v", record, err)
	}
	sql := registeredStatements(h)
	if !strings.Contains(sql, "excalibase_runtime_role") || strings.Contains(sql, "excalibase_runtime_lock") {
		t.Error("the runtime login was not created")
	}
	assertNoPassword(t, "the role SQL", sql+strings.Join(h.kube.ExecCommands, "\n"), map[string]string{roleRuntime: record["password"]})
	assertSetsVerifierOf(t, sql, roleRuntime, record["password"])
}

func TestRegistrationWithoutTheFeatureLocksAnyRuntimeLogin(t *testing.T) {
	h := newRegistrationHarness(t)
	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{ResetRolePasswords: true}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	if _, err := h.vault.Get(vaultCredentialPath(testRegProject, roleRuntime)); err == nil {
		t.Fatal("a runtime login was filed with the feature off")
	}
	sql := registeredStatements(h)
	if !strings.Contains(sql, "excalibase_runtime_lock") || strings.Contains(sql, "excalibase_runtime_role") {
		t.Error("an unfiled runtime login is not locked")
	}
}

// A project registered before the feature gets its login at the next reconcile
// pass; one that has it is left alone.
func TestReconcileProjectRoles_CreatesTheRuntimeLoginOfAnOlderProject(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.svc.SetFeatures(features.NewStatic(features.AppRuntimeLogin))
	// The scheduler login is already filed: only the runtime login is missing.
	h.vault.data[rotSchedulerCurrent] = map[string]string{"host": "h.local", "port": "5432", "database": "app",
		"username": roleScheduler, "password": testutil.FixturePassword(roleScheduler)}

	report := h.svc.ReconcileProjectRoles(context.Background())
	if report.Reconciled != 1 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v", report)
	}
	record := h.vault.data[vaultCredentialPath(rotProject, roleRuntime)]
	if record["username"] != roleRuntime || len(record["password"]) < 32 || record["database"] != "app" || record["host"] != "h.local" {
		t.Fatalf("runtime record = %v", record)
	}
	sql := strings.Join(h.kube.ExecStdin, "\n")
	if !strings.Contains(sql, "excalibase_runtime_role") {
		t.Error("the runtime login was not created")
	}
	assertSetsVerifierOf(t, sql, roleRuntime, record["password"])
	assertNoPassword(t, "the role SQL", sql, map[string]string{roleRuntime: record["password"]})

	filed := record["password"]
	execs := len(h.kube.ExecCommands)
	if again := h.svc.ReconcileProjectRoles(context.Background()); again.Reconciled != 0 || len(again.Failed) != 0 {
		t.Fatalf("second pass = %+v", again)
	}
	if h.vault.data[vaultCredentialPath(rotProject, roleRuntime)]["password"] != filed {
		t.Fatal("a second pass minted a new runtime password")
	}
	if len(h.kube.ExecCommands) != execs+1 {
		t.Fatalf("a second pass ran more than the auth probe: %q", h.kube.ExecCommands[execs:])
	}
}

func TestReconcileProjectRoles_LeavesTheRuntimeLoginAloneWhileItsFeatureIsOff(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.vault.data[rotSchedulerCurrent] = map[string]string{"host": "h.local", "port": "5432", "database": "app",
		"username": roleScheduler, "password": testutil.FixturePassword(roleScheduler)}
	if report := h.svc.ReconcileProjectRoles(context.Background()); report.Reconciled != 0 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if _, ok := h.vault.data[vaultCredentialPath(rotProject, roleRuntime)]; ok {
		t.Fatal("a runtime login was filed with the feature off")
	}
}

func TestRotateCredentialsReplacesTheRuntimeLogin(t *testing.T) {
	h := newRotationHarness(t)
	path := vaultCredentialPath(rotProject, roleRuntime)
	h.vault.data[path] = map[string]string{"host": "h.local", "port": "5432", "database": "app",
		"username": roleRuntime, "password": testutil.FixturePassword(roleRuntime)}
	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	if h.vault.data[path]["password"] == testutil.FixturePassword(roleRuntime) || h.vault.data[path]["username"] != roleRuntime {
		t.Fatalf("the runtime login was not rotated: %v", h.vault.data[path])
	}
	order := rotatedRoleOrder(t, h.kube.ExecStdin)
	if len(order) != 4 || order[2] != roleRuntime || order[3] != rotOwner {
		t.Fatalf("rotation order = %v, want the runtime login before the owner", order)
	}
}

// Deleting a project removes the login with the others.
func TestDatabaseLoginRolesIncludeTheRuntimeLogin(t *testing.T) {
	for _, role := range databaseLoginRoles {
		if role == roleRuntime {
			return
		}
	}
	t.Fatal("a project's teardown leaves the runtime login in vault")
}

func TestMySQLRuntimeLoginRecordCarriesTheServerCAAndIsRefiledWithIt(t *testing.T) {
	found := false
	for _, role := range mysqlTLSRoles {
		found = found || role == roleRuntime
	}
	if !found {
		t.Fatal("a renewed server CA would stay out of the runtime login's record")
	}
}

// A MySQL project created while the feature is on gets the login, over TLS,
// with the CA its clients verify the server against.
func TestProvisionMySQLFilesTheRuntimeLogin(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	svc.SetFeatures(features.NewStatic(features.AppRuntimeLogin))
	inst := provisionMySQL(t, svc, store, "")
	record, err := svc.vault.Get(vaultCredentialPath(inst.ProjectID, roleRuntime))
	if err != nil || record["username"] != roleRuntime || len(record["password"]) < 32 || record["sslrootcert"] == "" || record["engine"] != "mysql" {
		t.Fatalf("runtime record %v, %v", record, err)
	}
	statements := strings.Join(mock.ExecStdin, "\n")
	if !strings.Contains(statements, "CREATE USER IF NOT EXISTS 'excalibase_runtime'@'%'") ||
		!strings.Contains(statements, "GRANT ALL PRIVILEGES ON `app`.* TO 'excalibase_runtime'@'%'") {
		t.Errorf("the runtime user was not created with the project database only:\n%s", statements)
	}
	if strings.Contains(mock.ExecCommands[len(mock.ExecCommands)-1], record["password"]) {
		t.Error("the runtime password reached an exec argument")
	}
}

// An existing MySQL project gets its login at reconcile, once.
func TestReconcileProjectRoles_CreatesTheRuntimeUserOfAnOlderMySQLProject(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	if _, err := svc.vault.Get(vaultCredentialPath(inst.ProjectID, roleRuntime)); err == nil {
		t.Fatal("precondition: the project already has a runtime login")
	}
	svc.SetFeatures(features.NewStatic(features.AppRuntimeLogin))
	before := len(mock.ExecStdin)

	report := svc.ReconcileProjectRoles(context.Background())
	if report.Reconciled != 1 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v", report)
	}
	record, err := svc.vault.Get(vaultCredentialPath(inst.ProjectID, roleRuntime))
	if err != nil || len(record["password"]) < 32 || record["sslrootcert"] == "" {
		t.Fatalf("runtime record %v, %v", record, err)
	}
	if !strings.Contains(strings.Join(mock.ExecStdin[before:], "\n"), "GRANT ALL PRIVILEGES ON `app`.* TO 'excalibase_runtime'@'%'") {
		t.Error("the runtime user was not created")
	}
	// The platform users keep the passwords vault holds for them.
	owner, _ := svc.vault.Get(vaultCredentialPath(inst.ProjectID, roleAdmin))
	if !strings.Contains(strings.Join(mock.ExecStdin[before:], "\n"), "IDENTIFIED BY '"+owner["password"]+"'") {
		t.Error("the owner's password was not carried")
	}

	again := svc.ReconcileProjectRoles(context.Background())
	if again.Reconciled != 0 || len(again.Failed) != 0 || len(mock.ExecStdin) != before+1 {
		t.Fatalf("second pass %+v ran %d more scripts", again, len(mock.ExecStdin)-before-1)
	}
}
