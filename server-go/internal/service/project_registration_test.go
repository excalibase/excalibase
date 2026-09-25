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

const (
	testRegProject = "proj-reg1"
	testRegOrg     = "org-reg"
)

// registrationHarness wires a ProvisioningService with the collaborators
// RegisterProject touches, so each test can assert on one of them.
type registrationHarness struct {
	svc      *ProvisioningService
	store    *storage.FileSystemStore
	vault    *fakeVault
	kube     *k8s.MockClient
	pgdog    *fakePgDogStore
	activity *fakeActivityStore
	events   *fakePolicyPublisher
}

type fakePolicyPublisher struct{ events []domain.PolicyChangeEvent }

func (f *fakePolicyPublisher) PublishPolicyChange(_ context.Context, evt domain.PolicyChangeEvent) {
	f.events = append(f.events, evt)
}

func newRegistrationHarness(t *testing.T) *registrationHarness {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	kube := k8s.NewMockClient()
	kube.WildcardPodReady = true
	h := &registrationHarness{
		store:    store,
		vault:    newFakeVault(),
		kube:     kube,
		pgdog:    &fakePgDogStore{},
		activity: &fakeActivityStore{},
		events:   &fakePolicyPublisher{},
	}
	h.svc = NewProvisioningService(store, provisioner.NewFactory(), kube)
	h.svc.SetVault(h.vault)
	notifier, err := NewPgDogNotifier(h.pgdog, "")
	if err != nil {
		t.Fatalf("pgdog notifier: %v", err)
	}
	h.svc.SetPgDogNotifier(notifier)
	h.svc.SetActivityRecorder(NewActivityRecorder(ActivityRecorderConfig{Store: h.activity}))
	h.svc.SetProjectEventPublisher(h.events)
	return h
}

func restoredInstance() *domain.DatabaseInstance {
	port := 5432
	return &domain.DatabaseInstance{
		ProjectID:             testRegProject,
		ProjectName:           testRegProject,
		OrgID:                 testRegOrg,
		DBType:                domain.PostgreSQL,
		Tier:                  domain.Standard,
		DeploymentMode:        domain.ModeK8s,
		Namespace:             testRegOrg + "-" + testRegProject,
		Host:                  "h",
		Port:                  &port,
		DatabaseName:          "app",
		Username:              "app",
		Password:              "adminpass",
		RestoredFromProjectID: "proj-src",
		RestoredFromBackupID:  "backup-1",
	}
}

func TestRegisterProjectSavesActiveRowWithRestoreProvenance(t *testing.T) {
	h := newRegistrationHarness(t)

	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{ResetRolePasswords: true}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}

	saved, err := h.store.FindByProjectID(testRegProject)
	if err != nil || saved == nil {
		t.Fatalf("restored project not registered: %v", err)
	}
	if saved.Status != "ACTIVE" || saved.CurrentStage != domain.StageCompleted {
		t.Errorf("status/stage: %s/%s", saved.Status, saved.CurrentStage)
	}
	if saved.RestoredFromProjectID != "proj-src" || saved.RestoredFromBackupID != "backup-1" {
		t.Errorf("provenance lost: %+v", saved)
	}
	if saved.Tier != domain.Standard || saved.OrgID != testRegOrg {
		t.Errorf("tier/org not carried: %s/%s", saved.Tier, saved.OrgID)
	}
}

func TestRegisterProjectWritesVaultCredentials(t *testing.T) {
	h := newRegistrationHarness(t)

	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}

	for _, role := range []string{"admin", "auth_admin", "excalibase_app", "cdc_watcher"} {
		creds, err := h.vault.Get(vaultCredentialPath(testRegProject, role))
		if err != nil {
			t.Fatalf("vault %s: %v", role, err)
		}
		if creds["password"] == "" || creds["database"] != "app" {
			t.Errorf("vault %s incomplete: %v", role, creds)
		}
	}
}

func TestRegisterProjectResetsRolePasswordsToTheVaultValues(t *testing.T) {
	h := newRegistrationHarness(t)
	inst := restoredInstance()

	opts := RegistrationOptions{ResetRolePasswords: true, ResetAdminPassword: true}
	if err := h.svc.RegisterProject(context.Background(), inst, opts); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}

	sql := strings.Join(h.kube.ExecCommands, "\n")
	appCreds, _ := h.vault.Get(vaultCredentialPath(testRegProject, "excalibase_app"))
	if !strings.Contains(sql, alterRolePasswordSQL("excalibase_app", appCreds["password"])) {
		t.Fatalf("restored cluster must have its inherited role passwords reset to the vault value; sql=%s", sql)
	}
	if !strings.Contains(sql, "ALTER ROLE %I WITH LOGIN PASSWORD %L', "+sqlTextLiteral("app")+",") {
		t.Error("ResetAdminPassword must reset the admin role too")
	}
}

func TestRegisterProjectKeepsRolePasswordsOnAFreshCluster(t *testing.T) {
	h := newRegistrationHarness(t)
	inst := restoredInstance()
	inst.RestoredFromProjectID = ""

	if err := h.svc.RegisterProject(context.Background(), inst, RegistrationOptions{}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	if strings.Contains(strings.Join(h.kube.ExecCommands, "\n"), "ALTER ROLE") {
		t.Error("a freshly created cluster must not get password-reset statements")
	}
}

func TestRegisterProjectRegistersWithPgDog(t *testing.T) {
	h := newRegistrationHarness(t)

	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	if len(h.pgdog.databases) == 0 || len(h.pgdog.users) == 0 {
		t.Errorf("pgdog registration missing: dbs=%d users=%d", len(h.pgdog.databases), len(h.pgdog.users))
	}
}

func TestRegisterProjectPublishesAndRecordsActivity(t *testing.T) {
	h := newRegistrationHarness(t)

	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	if len(h.events.events) != 1 || h.events.events[0].ProjectID != testRegProject {
		t.Errorf("project-created event not published: %+v", h.events.events)
	}
	if h.events.events[0].Op != "create" {
		t.Errorf("event op: %q", h.events.events[0].Op)
	}
	if len(h.activity.touches) != 1 || h.activity.touches[0].projectID != testRegProject {
		t.Errorf("activity marker not recorded for the new project: %+v", h.activity.touches)
	}
}

func TestRegisterProjectFailsWhenRoleSetupFails(t *testing.T) {
	h := newRegistrationHarness(t)
	h.kube.WildcardExecError = errors.New("psql exploded")

	err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{})
	if err == nil {
		t.Fatal("expected role setup failure to fail registration")
	}
	if saved, _ := h.store.FindByProjectID(testRegProject); saved != nil {
		t.Error("no project row may be left behind when credentials could not be set up")
	}
	if len(h.vault.deletes) == 0 {
		t.Error("vault entries written before the failure must be rolled back")
	}
}

func TestRegisterProjectRejectsAnEmptyProject(t *testing.T) {
	h := newRegistrationHarness(t)
	if err := h.svc.RegisterProject(context.Background(), nil, RegistrationOptions{}); err == nil {
		t.Error("nil instance must be rejected")
	}
	if err := h.svc.RegisterProject(context.Background(), &domain.DatabaseInstance{}, RegistrationOptions{}); err == nil {
		t.Error("instance without a project id must be rejected")
	}
}

// Registering a project whose vault prefix already holds credentials must
// fail: those entries belong to a project that already exists, and
// overwriting them hands its clients a different database (EXC-415).
func TestRegisterProjectRefusesToOverwriteExistingVaultCredentials(t *testing.T) {
	h := newRegistrationHarness(t)
	existingPath := vaultCredentialPath(testRegProject, roleApp)
	h.vault.data[existingPath] = map[string]string{
		"host": "victim-rw", "username": roleApp, "password": "victim-app-password",
	}

	err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{})
	if !errors.Is(err, ErrProjectCredentialsExist) {
		t.Fatalf("err: got %v, want ErrProjectCredentialsExist", err)
	}
	if got := h.vault.data[existingPath]["password"]; got != "victim-app-password" {
		t.Errorf("existing credentials were overwritten: %q", got)
	}
	if saved, _ := h.store.FindByProjectID(testRegProject); saved != nil {
		t.Error("no project row may be written when registration is refused")
	}
}

// A vault that cannot be listed leaves the platform unable to prove the
// project id is free of credentials, so registration fails rather than
// writing over whatever is there.
func TestRegisterProjectFailsWhenExistingCredentialsCannotBeChecked(t *testing.T) {
	h := newRegistrationHarness(t)
	h.svc.SetVault(&errVault{err: errors.New("vault outage")})

	err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{})
	if err == nil || !strings.Contains(err.Error(), "list existing credentials") {
		t.Fatalf("err: got %v, want a failed credential pre-check", err)
	}
	if saved, _ := h.store.FindByProjectID(testRegProject); saved != nil {
		t.Error("no project row may be written when the vault pre-check fails")
	}
}
