package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func setupProvisioningTest(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	// Enable wildcards so provisioning tests work with server-generated project refs
	// (tests can't pre-populate pod/secret entries keyed by unknown ref).
	mock.WildcardPodReady = true
	mock.WildcardSecret = map[string][]byte{
		"username": []byte("app"),
		"password": []byte("testpassword123"),
		"dbname":   []byte("app"),
	}
	pgProv := provisioner.NewPostgreSQLProvisioner(mock, "")
	factory := provisioner.NewFactory(pgProv)
	svc := NewProvisioningService(store, factory, mock)
	return svc, store, mock
}

func TestProvisionSuccess(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "test-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if resp.Status != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", resp.Status)
	}
	if resp.CurrentStage != domain.StageCompleted {
		t.Errorf("stage: got %s, want COMPLETED", resp.CurrentStage)
	}
	if resp.ProjectName != "test-db" {
		t.Errorf("ProjectName: got %q, want %q", resp.ProjectName, "test-db")
	}
	if resp.Namespace != "org1-"+resp.ProjectID {
		t.Errorf("namespace: got %s, want %s", resp.Namespace, "org1-"+resp.ProjectID)
	}
}

// Same display name no longer rejected — project IDs are generated opaque refs.
// The test stays as a smoke test that existing instances don't block new ones.
func TestProvisionSameDisplayNameDoesNotBlock(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.WildcardPodReady = true
	mock.WildcardSecret = map[string][]byte{
		"username": []byte("app"),
		"password": []byte("testpassword123"),
		"dbname":   []byte("app"),
	}
	store.Save(&domain.DatabaseInstance{ProjectID: "proj-aaaaaaaaaa", ProjectName: "Blog", OrgID: "org1", Status: "ACTIVE"})

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "Blog",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Standard,
	})
	if err != nil {
		t.Fatalf("same display name should be allowed: %v", err)
	}
	if resp.ProjectID == "proj-aaaaaaaaaa" {
		t.Errorf("ProjectID should be newly generated, got %q", resp.ProjectID)
	}
}

func TestProvisionExceedsFreeTierLimit(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)

	// Org already has 1 project (FREE tier max)
	store.Save(&domain.DatabaseInstance{ProjectID: "proj-existing01", OrgID: "org1", Status: "ACTIVE"})

	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "second-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err == nil {
		t.Error("expected error for exceeding FREE tier project limit")
	}
	if err != nil && !strings.Contains(err.Error(), "maximum") {
		t.Errorf("expected max projects error, got: %v", err)
	}
}

func TestProvisionBackupNotAllowedOnFreeTier(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)

	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "backup-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
		Backup:      &domain.BackupSettings{Enabled: true},
	})
	if err == nil {
		t.Error("expected error for backup on FREE tier")
	}
	if err != nil && !strings.Contains(err.Error(), "not available") {
		t.Errorf("expected backup not available error, got: %v", err)
	}
}

func TestProvisionStandardTierAllowsMultipleProjects(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)

	// Org already has 1 project but STANDARD allows 5
	store.Save(&domain.DatabaseInstance{ProjectID: "proj-stdfirst01", OrgID: "org1", Status: "ACTIVE"})

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "std-second",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Standard,
	})
	if err != nil {
		t.Fatalf("STANDARD tier should allow 2nd project: %v", err)
	}
	if resp.Status != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", resp.Status)
	}
}

func TestProvisionUnsupportedType(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)

	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "test",
		OrgID:       "org1",
		DBType:      "REDIS", // unsupported
		Tier:        domain.Free,
	})
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestDeprovision(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock("del-db", "org1-del-db", 1)

	store.Save(&domain.DatabaseInstance{
		ProjectID: "del-db",
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Namespace: "org1-del-db",
		Status:    "ACTIVE",
	})

	if err := svc.Deprovision(context.Background(), "del-db"); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}

	inst, _ := store.FindByProjectID("del-db")
	if inst != nil {
		t.Error("instance should be deleted")
	}
}

func TestDeprovisionNotFound(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	err := svc.Deprovision(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestDeprovisionDeletionProtection(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	protected := true
	store.Save(&domain.DatabaseInstance{
		ProjectID:          "protected-db",
		DBType:             domain.PostgreSQL,
		Status:             "ACTIVE",
		DeletionProtection: &protected,
	})

	err := svc.Deprovision(context.Background(), "protected-db")
	if err == nil {
		t.Error("expected error for deletion-protected project")
	}
}

func TestGetCredentials(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	port := 5432
	store.Save(&domain.DatabaseInstance{
		ProjectID:    "cred-db",
		Host:         "host.local",
		Port:         &port,
		DatabaseName: "mydb",
		Username:     "user",
		Password:     "pass",
		SSLMode:      "require",
		Status:       "ACTIVE",
	})

	creds, err := svc.GetCredentials("cred-db")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.Host != "host.local" {
		t.Errorf("host: got %s", creds.Host)
	}
	if creds.Password != "pass" {
		t.Errorf("password not returned")
	}
	if creds.ConnectionURL == "" {
		t.Error("connectionUrl should be set")
	}
}

func TestSetDeletionProtection(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	store.Save(&domain.DatabaseInstance{ProjectID: "dp-db", Status: "ACTIVE"})

	if err := svc.SetDeletionProtection("dp-db", true); err != nil {
		t.Fatalf("SetDeletionProtection: %v", err)
	}

	inst, _ := store.FindByProjectID("dp-db")
	if inst.DeletionProtection == nil || !*inst.DeletionProtection {
		t.Error("deletion protection should be enabled")
	}
}

func TestGetAllInstances(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	store.Save(&domain.DatabaseInstance{ProjectID: "a", Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "b", Status: "ACTIVE"})

	all, _ := svc.GetAllInstances()
	if len(all) != 2 {
		t.Errorf("expected 2, got %d", len(all))
	}
}

// --- Input validation ---

func TestProvisionValidation_EmptyProjectName(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err == nil || !strings.Contains(err.Error(), "project name") {
		t.Errorf("expected project name error, got: %v", err)
	}
}

func TestProvisionValidation_ProjectNameTooLong(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: strings.Repeat("a", 101),
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err == nil || !strings.Contains(err.Error(), "project name") {
		t.Errorf("expected project name length error, got: %v", err)
	}
}

func TestProvisionValidation_EmptyOrgID(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "blog",
		OrgID:       "",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err == nil || !strings.Contains(err.Error(), "org") {
		t.Errorf("expected org error, got: %v", err)
	}
}

func TestProvisionValidation_InvalidOrgIDChars(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "blog",
		OrgID:       "Invalid_Org!",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err == nil || !strings.Contains(err.Error(), "org") {
		t.Errorf("expected org chars error, got: %v", err)
	}
}

func TestProvisionValidation_InvalidPostgresVersion(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName:     "blog",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Tier:            domain.Free,
		PostgresVersion: "9.2",
	})
	if err == nil || !strings.Contains(err.Error(), "postgres version") {
		t.Errorf("expected postgres version error, got: %v", err)
	}
}

func TestProvisionValidation_ValidPostgresVersion(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName:     "blog",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Tier:            domain.Free,
		PostgresVersion: "17",
	})
	if err != nil {
		t.Errorf("version 17 should be valid, got: %v", err)
	}
}

func TestProvisionValidation_BackupRetentionOutOfRange(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "blog",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Standard,
		Backup:      &domain.BackupSettings{Enabled: true, Retention: 9999},
	})
	if err == nil || !strings.Contains(err.Error(), "retention") {
		t.Errorf("expected retention error, got: %v", err)
	}
}

// --- Generated project ref (opaque identifier) ---

var projectRefPattern = regexp.MustCompile(`^proj-[a-z0-9]{10}$`)

func TestProvision_GeneratesOpaqueProjectRef(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	// Mock is keyed by namespace, and namespace uses the generated ref.
	// Use WildcardPodReady so any project-id pod is considered ready.
	mock.WildcardPodReady = true
	mock.WildcardSecret = map[string][]byte{
		"username": []byte("app"),
		"password": []byte("testpassword123"),
		"dbname":   []byte("app"),
	}

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "My Cool App 🚀",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !projectRefPattern.MatchString(resp.ProjectID) {
		t.Errorf("ProjectID: got %q, want match %s", resp.ProjectID, projectRefPattern)
	}
	if resp.ProjectID == "My Cool App 🚀" {
		t.Errorf("ProjectID must be generated, not the display name")
	}

	inst, _ := store.FindByProjectID(resp.ProjectID)
	if inst == nil {
		t.Fatalf("instance not persisted under generated ID %q", resp.ProjectID)
	}
	if inst.ProjectName != "My Cool App 🚀" {
		t.Errorf("inst.ProjectName: got %q, want %q", inst.ProjectName, "My Cool App 🚀")
	}
	if inst.ProjectID != resp.ProjectID {
		t.Errorf("inst.ProjectID: got %q, want %q", inst.ProjectID, resp.ProjectID)
	}
}

func TestProvision_SameDisplayNameAllowedUniqueRefs(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	mock.WildcardPodReady = true
	mock.WildcardSecret = map[string][]byte{
		"username": []byte("app"),
		"password": []byte("testpassword123"),
		"dbname":   []byte("app"),
	}

	resp1, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "Blog",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Standard,
	})
	if err != nil {
		t.Fatalf("Provision 1: %v", err)
	}
	resp2, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "Blog", // same display name — must be allowed
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Standard,
	})
	if err != nil {
		t.Fatalf("Provision 2 (same display name): %v", err)
	}
	if resp1.ProjectID == resp2.ProjectID {
		t.Errorf("ProjectIDs must be unique, both=%q", resp1.ProjectID)
	}
}

func TestProvision_NamespaceUsesGeneratedRef(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	mock.WildcardPodReady = true
	mock.WildcardSecret = map[string][]byte{
		"username": []byte("app"),
		"password": []byte("testpassword123"),
		"dbname":   []byte("app"),
	}

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "App",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	wantNamespace := "org1-" + resp.ProjectID
	if resp.Namespace != wantNamespace {
		t.Errorf("namespace: got %q, want %q", resp.Namespace, wantNamespace)
	}
	if !mock.Namespaces[wantNamespace] {
		t.Errorf("K8s namespace %q not created", wantNamespace)
	}
}

// --- Fake vault for rollback tests ---

type fakeVault struct {
	data    map[string]map[string]string
	deletes []string
}

func newFakeVault() *fakeVault { return &fakeVault{data: map[string]map[string]string{}} }

func (f *fakeVault) Get(p string) (map[string]string, error) {
	if d, ok := f.data[p]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeVault) Put(p string, d map[string]string) error { f.data[p] = d; return nil }
func (f *fakeVault) Delete(p string) error {
	f.deletes = append(f.deletes, p)
	delete(f.data, p)
	return nil
}
func (f *fakeVault) List(prefix string) ([]string, error) { return nil, nil }
func (f *fakeVault) Sealed() bool                         { return false }
func (f *fakeVault) GetPublicKey() (string, error)        { return "", nil }

// --- Rollback on failure ---

func TestProvisionFailure_PopulatesFailureStageAndStep(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.CRDError = errors.New("forbidden: CNPG CRD missing")

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "fail-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("Provision should return response+nil err, got err: %v", err)
	}
	if resp.Status != "FAILED" {
		t.Errorf("resp.Status: got %s, want FAILED", resp.Status)
	}
	if resp.FailureStage != domain.StageCRDDeployment {
		t.Errorf("resp.FailureStage: got %s, want CRD_DEPLOYMENT", resp.FailureStage)
	}
	if resp.FailureStep == "" {
		t.Error("resp.FailureStep should be set")
	}

	inst, _ := store.FindByProjectID(resp.ProjectID)
	if inst == nil {
		t.Fatal("instance not persisted")
	}
	if inst.FailureStage != domain.StageCRDDeployment {
		t.Errorf("inst.FailureStage: got %s", inst.FailureStage)
	}
	if inst.FailureStep == "" {
		t.Error("inst.FailureStep should be set")
	}
	if inst.FailureReason == "" {
		t.Error("inst.FailureReason should be set")
	}
}

func TestProvisionFailure_RollbackDeletesNamespace(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	mock.CRDError = errors.New("forbidden")

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "rb-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if mock.Namespaces["org1-"+resp.ProjectID] {
		t.Error("namespace should be deleted by rollback")
	}
}

func TestProvisionFailure_PersistsRollbackLog(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.CRDError = errors.New("forbidden")

	resp, _ := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "log-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	inst, _ := store.FindByProjectID(resp.ProjectID)
	if inst == nil {
		t.Fatal("instance not persisted")
	}
	if inst.RollbackLog == "" {
		t.Fatal("RollbackLog should be populated JSON")
	}
	var results []provisioner.CleanupResult
	if err := json.Unmarshal([]byte(inst.RollbackLog), &results); err != nil {
		t.Fatalf("RollbackLog not valid JSON: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 cleanup result, got %d: %+v", len(results), results)
	}
	if !results[0].OK {
		t.Errorf("cleanup should be OK: %+v", results[0])
	}
	if !strings.Contains(results[0].Name, "namespace") {
		t.Errorf("cleanup name should mention namespace: %q", results[0].Name)
	}
}

func TestProvisionFailure_RollsBackVaultWritesOnRoleCreationFailure(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	// Force pod exec to fail for any pod — happens inside createProjectRoles
	// AFTER the admin vault entry has been written.
	mock.WildcardExecError = errors.New("psql permission denied")

	v := newFakeVault()
	svc.SetVault(v)

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "vault-fail",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Status != "FAILED" {
		t.Errorf("status: got %s, want FAILED", resp.Status)
	}
	if resp.FailureStage != domain.StageRoleCreation {
		t.Errorf("FailureStage: got %s, want ROLE_CREATION", resp.FailureStage)
	}

	// Admin vault entry must be rolled back (created then deleted)
	adminPath := "projects/org1/" + resp.ProjectID + "/credentials/admin"
	if _, stillThere := v.data[adminPath]; stillThere {
		t.Errorf("vault admin entry should be deleted by rollback, still at %s", adminPath)
	}
	// And the cleanup was actually called (shows up in deletes)
	foundAdminDelete := false
	for _, d := range v.deletes {
		if d == adminPath {
			foundAdminDelete = true
		}
	}
	if !foundAdminDelete {
		t.Errorf("expected Delete(%q), got deletes=%v", adminPath, v.deletes)
	}

	// Namespace also rolled back (LIFO: vault first, then namespace)
	if mock.Namespaces["org1-"+resp.ProjectID] {
		t.Error("namespace should be deleted by rollback")
	}

	// Rollback log should contain at least 2 results (vault admin + namespace)
	inst, _ := store.FindByProjectID(resp.ProjectID)
	if inst == nil {
		t.Fatal("instance not persisted")
	}
	var results []provisioner.CleanupResult
	if err := json.Unmarshal([]byte(inst.RollbackLog), &results); err != nil {
		t.Fatalf("RollbackLog not valid JSON: %v", err)
	}
	if len(results) < 2 {
		t.Errorf("expected >=2 rollback results, got %d: %+v", len(results), results)
	}
}

func TestProvisionFailure_NamespaceFailureHasNoRollbackLog(t *testing.T) {
	// When namespace creation itself fails, no cleanups are registered.
	// Rollback log should be empty-array JSON (or omitted).
	svc, store, mock := setupProvisioningTest(t)
	mock.NamespaceError = errors.New("quota exceeded")

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "ns-fail",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.FailureStage != domain.StageNamespaceCreation {
		t.Errorf("FailureStage: got %s", resp.FailureStage)
	}
	inst, _ := store.FindByProjectID(resp.ProjectID)
	if inst == nil {
		t.Fatal("instance not persisted")
	}
	if inst.RollbackLog != "" && inst.RollbackLog != "[]" && inst.RollbackLog != "null" {
		var results []provisioner.CleanupResult
		if err := json.Unmarshal([]byte(inst.RollbackLog), &results); err != nil {
			t.Fatalf("RollbackLog not valid JSON: %v, raw=%q", err, inst.RollbackLog)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 cleanups for pre-namespace failure, got %d", len(results))
		}
	}
}

func TestGetInstancesByOwner(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	store.Save(&domain.DatabaseInstance{ProjectID: "a", OwnerID: "user-1", Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "b", OwnerID: "user-1", Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "c", OwnerID: "user-2", Status: "ACTIVE"})

	owned, err := svc.GetInstancesByOwner("user-1")
	if err != nil {
		t.Fatalf("GetInstancesByOwner: %v", err)
	}
	if len(owned) != 2 {
		t.Errorf("expected 2, got %d", len(owned))
	}
	for _, inst := range owned {
		if inst.OwnerID != "user-1" {
			t.Errorf("unexpected owner: %s", inst.OwnerID)
		}
	}

	// Empty result for unknown owner
	empty, err := svc.GetInstancesByOwner("nobody")
	if err != nil {
		t.Fatalf("GetInstancesByOwner empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0, got %d", len(empty))
	}
}
