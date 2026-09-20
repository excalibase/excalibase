package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Enabling DocumentDB on the new database (EXC-409). The image carries the
// files and the cluster preloads the libraries; what is left is creating the
// extension in the project's own database and refusing to call the project
// finished unless it is really there.

// documentDBProject is a project created with DocumentDB on a capable major.
func documentDBProject() *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID:       "proj-docsvc001",
		OrgID:           "org-doc",
		DBType:          domain.PostgreSQL,
		Tier:            domain.Free,
		Namespace:       "org-doc-proj-docsvc001",
		DatabaseName:    "appdb",
		DeploymentMode:  domain.ModeK8s,
		PostgresVersion: "17",
		DocumentDB:      true,
	}
}

// documentDBService builds a service whose only capability is executing SQL.
func documentDBService(t *testing.T, kube k8s.KubeClient) *ProvisioningService {
	t.Helper()
	return NewProvisioningService(documentDBStore(t), provisioner.NewFactory(), kube)
}

// documentDBStore is an empty project store on disk, discarded with the test.
func documentDBStore(t *testing.T) storage.InstanceStore {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("project store: %v", err)
	}
	return store
}

// idleContext is a provision context that records nothing, for the tests that
// only care what SQL ran.
func idleContext() *provisioner.ProvisionContext {
	return provisioner.NewProvisionContext(func(domain.ProvisioningStage) {}, func(string) {})
}

// execCommandsMentioning returns the recorded exec argv containing a fragment.
func execCommandsMentioning(kube *k8s.MockClient, fragment string) []string {
	matched := make([]string, 0, len(kube.ExecCommands))
	for _, cmd := range kube.ExecCommands {
		if strings.Contains(cmd, fragment) {
			matched = append(matched, cmd)
		}
	}
	return matched
}

// The extension is created in the project's own database, by name, with
// CASCADE so its dependencies come with it.
func TestEnableDocumentDBCreatesTheExtensionInTheProjectsDatabase(t *testing.T) {
	kube := k8s.NewMockClient()
	svc := documentDBService(t, kube)
	inst := documentDBProject()

	if err := svc.enableDocumentDB(context.Background(), inst, idleContext()); err != nil {
		t.Fatalf("enableDocumentDB: %v", err)
	}

	created := execCommandsMentioning(kube, "CREATE EXTENSION")
	if len(created) != 1 {
		t.Fatalf("CREATE EXTENSION ran %d times: %v", len(created), kube.ExecCommands)
	}
	for _, want := range []string{"documentdb", "CASCADE", "-d appdb"} {
		if !strings.Contains(created[0], want) {
			t.Errorf("CREATE EXTENSION command %q is missing %q", created[0], want)
		}
	}
}

// psql exits zero on a failed statement unless told otherwise, so an exec that
// "succeeded" proves nothing without it. Every statement this step runs
// carries ON_ERROR_STOP.
func TestEnableDocumentDBStopsOnTheFirstSQLError(t *testing.T) {
	kube := k8s.NewMockClient()
	svc := documentDBService(t, kube)

	if err := svc.enableDocumentDB(context.Background(), documentDBProject(), idleContext()); err != nil {
		t.Fatalf("enableDocumentDB: %v", err)
	}

	if len(kube.ExecCommands) == 0 {
		t.Fatal("nothing was executed")
	}
	for _, cmd := range kube.ExecCommands {
		if !strings.Contains(cmd, "ON_ERROR_STOP=1") {
			t.Errorf("command %q runs without ON_ERROR_STOP", cmd)
		}
	}
}

// Creating the extension is not the same as having it. The step asks the
// database what it actually holds and fails when the answer is not the
// extension, rather than reporting a project as DocumentDB on the strength of
// a command that returned zero.
func TestEnableDocumentDBConfirmsTheExtensionIsInstalled(t *testing.T) {
	kube := k8s.NewMockClient()
	svc := documentDBService(t, kube)

	if err := svc.enableDocumentDB(context.Background(), documentDBProject(), idleContext()); err != nil {
		t.Fatalf("enableDocumentDB: %v", err)
	}

	if len(execCommandsMentioning(kube, "pg_extension")) == 0 {
		t.Errorf("the extension was never confirmed installed: %v", kube.ExecCommands)
	}
}

// A project that did not ask for DocumentDB gets nothing run against its
// database at all.
func TestEnableDocumentDBRunsNothingForAProjectThatDidNotAskForIt(t *testing.T) {
	kube := k8s.NewMockClient()
	svc := documentDBService(t, kube)
	inst := documentDBProject()
	inst.DocumentDB = false

	if err := svc.enableDocumentDB(context.Background(), inst, idleContext()); err != nil {
		t.Fatalf("enableDocumentDB: %v", err)
	}

	if len(kube.ExecCommands) != 0 {
		t.Errorf("a project without DocumentDB had SQL run against it: %v", kube.ExecCommands)
	}
}

// A refusal, not a silent skip: the platform that cannot run SQL cannot
// install the extension, and a project reported ACTIVE-with-DocumentDB on a
// database nothing was ever run against is the failure this ticket exists to
// avoid.
func TestEnableDocumentDBRefusesWhenThereIsNoWayToRunSQL(t *testing.T) {
	svc := documentDBService(t, nil)

	err := svc.enableDocumentDB(context.Background(), documentDBProject(), idleContext())
	if !errors.Is(err, ErrDocumentDBNotInstallable) {
		t.Fatalf("enableDocumentDB with no exec client: got %v, want ErrDocumentDBNotInstallable", err)
	}
}

// The major is checked again here rather than trusted from the request. A row
// that reached this step claiming DocumentDB on a major whose image cannot
// carry it is a bug upstream of us, and running CREATE EXTENSION anyway would
// turn it into a confusing psql error instead of a clear one.
func TestEnableDocumentDBRefusesAMajorThatCannotCarryIt(t *testing.T) {
	kube := k8s.NewMockClient()
	svc := documentDBService(t, kube)
	inst := documentDBProject()
	inst.PostgresVersion = "14"

	err := svc.enableDocumentDB(context.Background(), inst, idleContext())
	if !errors.Is(err, ErrDocumentDBNotAvailableOnMajor) {
		t.Fatalf("enableDocumentDB on 14: got %v, want ErrDocumentDBNotAvailableOnMajor", err)
	}
	if len(kube.ExecCommands) != 0 {
		t.Errorf("SQL ran for a major that cannot carry the extension: %v", kube.ExecCommands)
	}
}

// A database that refuses the statement fails the step, so the caller can
// fail the provision rather than record a project that is not what it says.
func TestEnableDocumentDBFailsWhenTheDatabaseRefusesTheStatement(t *testing.T) {
	kube := k8s.NewMockClient()
	kube.WildcardExecError = errors.New("could not open extension control file")
	svc := documentDBService(t, kube)

	err := svc.enableDocumentDB(context.Background(), documentDBProject(), idleContext())
	if err == nil {
		t.Fatal("a refused CREATE EXTENSION was reported as success")
	}
	if !strings.Contains(err.Error(), "documentdb") {
		t.Errorf("the failure does not say what could not be enabled: %v", err)
	}
}

// RegisterProject is the one path that makes a project usable. A project whose
// extension could not be enabled must not come out of it ACTIVE, and must not
// leave a row behind claiming it is.
func TestRegisterProjectFailsWhenDocumentDBCannotBeEnabled(t *testing.T) {
	kube := k8s.NewMockClient()
	kube.WildcardExecError = errors.New("could not open extension control file")
	store := documentDBStore(t)
	svc := NewProvisioningService(store, provisioner.NewFactory(), kube)

	inst := documentDBProject()
	err := svc.RegisterProject(context.Background(), inst, RegistrationOptions{})
	if err == nil {
		t.Fatal("registration succeeded for a project whose extension was never installed")
	}
	if inst.Status == "ACTIVE" {
		t.Error("the project was marked ACTIVE despite the extension failing")
	}
	if stored, _ := store.FindByProjectID(inst.ProjectID); stored != nil {
		t.Error("a project row was left behind for a failed DocumentDB enable")
	}
}

// The ordinary project is untouched by any of this: registration for a
// project without DocumentDB runs no extension SQL and still completes.
func TestRegisterProjectWithoutDocumentDBRunsNoExtensionSQL(t *testing.T) {
	kube := k8s.NewMockClient()
	store := documentDBStore(t)
	svc := NewProvisioningService(store, provisioner.NewFactory(), kube)

	inst := documentDBProject()
	inst.DocumentDB = false
	if err := svc.RegisterProject(context.Background(), inst, RegistrationOptions{}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}

	if len(execCommandsMentioning(kube, "CREATE EXTENSION")) != 0 {
		t.Errorf("extension SQL ran for a plain project: %v", kube.ExecCommands)
	}
	if inst.Status != "ACTIVE" {
		t.Errorf("status: got %q, want ACTIVE", inst.Status)
	}
}

// One credential, two protocols (EXC-409).
//
// The gateway authenticates a Mongo client against the ordinary PostgreSQL
// SCRAM verifier, so the project's own credential already authenticates over
// both. What it lacks is authorisation, and that is one membership.

func TestEnableDocumentDBGrantsTheProjectsOwnCredentialMongoAccess(t *testing.T) {
	kube := k8s.NewMockClient()
	svc := documentDBService(t, kube)
	inst := documentDBProject()

	if err := svc.enableDocumentDB(context.Background(), inst, idleContext()); err != nil {
		t.Fatalf("enableDocumentDB: %v", err)
	}

	granted := execCommandsMentioning(kube, "GRANT")
	if len(granted) != 1 {
		t.Fatalf("GRANT ran %d times: %v", len(granted), kube.ExecCommands)
	}
	for _, want := range []string{"documentdb_admin_role", inst.Username} {
		if !strings.Contains(granted[0], want) {
			t.Errorf("the grant is missing %q: %s", want, granted[0])
		}
	}
}

// No second identity is created and none is filed anywhere: a DocumentDB
// project has exactly the credentials every other project has.
func TestEnableDocumentDBCreatesNoSecondIdentity(t *testing.T) {
	kube := k8s.NewMockClient()
	svc := documentDBService(t, kube)

	if err := svc.enableDocumentDB(context.Background(), documentDBProject(), idleContext()); err != nil {
		t.Fatalf("enableDocumentDB: %v", err)
	}

	if len(execCommandsMentioning(kube, "create_user")) != 0 {
		t.Errorf("a second Mongo identity was created: %v", kube.ExecCommands)
	}
	if len(execCommandsMentioning(kube, "documentdb_admin'")) != 0 {
		t.Errorf("a documentdb_admin identity survives: %v", kube.ExecCommands)
	}
}

// The role name is quoted, so a project whose owner was named something
// awkward cannot turn the grant into another statement.
func TestDocumentDBGrantQuotesTheRoleName(t *testing.T) {
	sql := documentDBGrantSQL(`odd"name`)
	if !strings.Contains(sql, `"odd""name"`) {
		t.Errorf("the role name is not safely quoted: %s", sql)
	}
}

// Rotation needs no DocumentDB special case, and this test exists to keep it
// that way. The gateway authenticates a Mongo client against the ordinary
// PostgreSQL SCRAM verifier in pg_authid, so the ALTER USER the rotation path
// already runs moves both protocols in one statement — proved against a live
// gateway in internal/schema's two-protocol test. A future change that routed
// DocumentDB rotation through DocumentDB's own API, or added a second
// credential to rotate, would be the thing that could leave a project
// reachable one way and not the other.
func TestRotationNeedsNoDocumentDBSpecialCase(t *testing.T) {
	kube := k8s.NewMockClient()
	svc := NewProvisioningService(documentDBStore(t), provisioner.NewFactory(), kube)
	inst := documentDBProject()

	if err := svc.alterRolePassword(context.Background(), inst, inst.Username, "new-secret"); err != nil {
		t.Fatalf("alterRolePassword: %v", err)
	}

	if len(kube.ExecStdin) != 1 {
		t.Fatalf("rotation ran %d statements: %v", len(kube.ExecStdin), kube.ExecStdin)
	}
	statement := kube.ExecStdin[0]
	if !strings.Contains(statement, "ALTER USER") {
		t.Fatalf("rotation no longer uses ALTER USER, which is what carries the Mongo credential: %s", statement)
	}
	if strings.Contains(statement, "documentdb") {
		t.Errorf("rotation has grown a DocumentDB special case: %s", statement)
	}
}
