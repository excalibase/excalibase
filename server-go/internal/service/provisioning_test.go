package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	testDBName         = "test-db"
	testProvisionFmt   = "Provision: %v"
	testDelDB          = "del-db"
	testDeprovisionFmt = "Deprovision: %v"
	testVaultDel       = "vault-del"
	testLegacyProj     = "legacy-proj"
	testVaultErr       = "vault-err"
	testSealedDel      = "sealed-del"
	testCoolApp        = "My Cool App 🚀"
	testNotPersisted   = "instance not persisted"
	testUnexpErrFmt    = "unexpected err: %v"
	testUser1          = "user-1"
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

// TestTierResourceFootprint pins the CPU/memory math the /api/capacity
// endpoint and the pre-flight check both depend on. If new sidecars are
// added (e.g. shadow-tables daemon), update the expected numbers.
func TestTierResourceFootprint(t *testing.T) {
	cases := []struct {
		name       string
		tier       config.TierConfig
		wantCPU    int64 // milliCPU
		wantMemMiB int64 // megabytes for readability
	}{
		// FREE: 0.5 CPU * 1 instance + 60m sidecars = 560m
		// 512Mi * 1 + 192Mi sidecars = 704Mi
		{"free", config.TierConfig{Instances: 1, CPU: "0.5", Memory: "512Mi"}, 560, 704},
		// STANDARD: 2 CPU * 3 + 60m = 6060m; 4Gi * 3 + 192Mi = 12480Mi
		{"standard", config.TierConfig{Instances: 3, CPU: "2", Memory: "4Gi"}, 6060, 12480},
		// ENTERPRISE: 4 CPU * 5 + 60m = 20060m; 16Gi * 5 + 192Mi = 82112Mi
		{"enterprise", config.TierConfig{Instances: 5, CPU: "4", Memory: "16Gi"}, 20060, 82112},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cpu, mem, err := TierResourceFootprint(c.tier)
			if err != nil {
				t.Fatalf("TierResourceFootprint: %v", err)
			}
			if cpu != c.wantCPU {
				t.Errorf("cpu: got %dm, want %dm", cpu, c.wantCPU)
			}
			gotMiB := mem / (1024 * 1024)
			if gotMiB != c.wantMemMiB {
				t.Errorf("memory: got %dMiB, want %dMiB", gotMiB, c.wantMemMiB)
			}
		})
	}
}

// TestProvision_RefusesWhenClusterFull confirms the capacity pre-flight
// blocks new provisions instead of letting them fail at WAITING_FOR_READY
// 5 minutes later.
func TestProvision_RefusesWhenClusterFull(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	// Cluster has 1 CPU + 1Gi total but 950m + 900Mi already requested —
	// not enough headroom for FREE tier (500m + 512Mi + 60m sidecars).
	mock.Capacity = k8s.ClusterCapacity{
		AllocatableCPUMilli: 1000,
		AllocatableMemBytes: 1024 * 1024 * 1024,
		RequestedCPUMilli:   950,
		RequestedMemBytes:   900 * 1024 * 1024,
	}
	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "no-room",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err == nil {
		t.Fatalf("expected capacity error, got resp=%+v", resp)
	}
	if !strings.Contains(err.Error(), "not enough capacity") {
		t.Errorf("user-facing error should say 'not enough capacity': got %v", err)
	}
}

// TestProvision_AcceptsWhenClusterHasRoom confirms a healthy cluster passes
// the capacity check and proceeds to actual provisioning.
func TestProvision_AcceptsWhenClusterHasRoom(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	mock.Capacity = k8s.ClusterCapacity{
		AllocatableCPUMilli: 24000,
		AllocatableMemBytes: 64 * 1024 * 1024 * 1024,
		RequestedCPUMilli:   2000,
		RequestedMemBytes:   4 * 1024 * 1024 * 1024,
	}
	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "fits",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
}

func TestProvisionSuccess(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: testDBName,
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf(testProvisionFmt, err)
	}
	if resp.Status != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", resp.Status)
	}
	if resp.CurrentStage != domain.StageCompleted {
		t.Errorf("stage: got %s, want COMPLETED", resp.CurrentStage)
	}
	if resp.ProjectName != testDBName {
		t.Errorf("ProjectName: got %q, want %q", resp.ProjectName, testDBName)
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
	store.Create(&domain.DatabaseInstance{ProjectID: "proj-aaaaaaaaaa", ProjectName: "Blog", OrgID: "org1", Status: "ACTIVE"})

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
	store.Create(&domain.DatabaseInstance{ProjectID: "proj-existing01", OrgID: "org1", Status: "ACTIVE"})

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
	store.Create(&domain.DatabaseInstance{ProjectID: "proj-stdfirst01", OrgID: "org1", Status: "ACTIVE"})

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

// TestProvision_SetsDeploymentMode confirms the provisioning service
// stamps inst.DeploymentMode at provision time. Without it, the
// adapter dispatch in BackupService can't tell k8s from docker
// instances and falls back to the zero value (""). See DOCKER_BACKUP_IMPL.md §0.
func TestProvision_SetsDeploymentMode_K8s(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	svc.SetDefaultDeploymentMode(domain.ModeK8s)

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "k8s-mode-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	got, _ := store.FindByProjectID(resp.ProjectID)
	if got == nil {
		t.Fatal("instance not persisted")
	}
	if got.DeploymentMode != domain.ModeK8s {
		t.Errorf("deploymentMode: got %q, want k8s", got.DeploymentMode)
	}
}

func TestProvision_SetsDeploymentMode_Docker(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	svc.SetDefaultDeploymentMode(domain.ModeDocker)

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "docker-mode-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	got, _ := store.FindByProjectID(resp.ProjectID)
	if got == nil {
		t.Fatal("instance not persisted")
	}
	if got.DeploymentMode != domain.ModeDocker {
		t.Errorf("deploymentMode: got %q, want docker", got.DeploymentMode)
	}
}

// Default — no SetDefaultDeploymentMode call — must still produce
// ModeK8s so existing callers that don't know about the new setter
// keep working safely.
func TestProvision_DefaultsToK8s_WhenUnset(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "default-mode-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	got, _ := store.FindByProjectID(resp.ProjectID)
	if got.DeploymentMode != domain.ModeK8s {
		t.Errorf("default deploymentMode: got %q, want k8s", got.DeploymentMode)
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
	mock.SetupPostgreSQLMock(testDelDB, "org1-del-db", 1)

	store.Create(&domain.DatabaseInstance{
		ProjectID: testDelDB,
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Namespace: "org1-del-db",
		Status:    "ACTIVE",
	})

	if err := svc.Deprovision(context.Background(), testDelDB); err != nil {
		t.Fatalf(testDeprovisionFmt, err)
	}

	inst, _ := store.FindByProjectID(testDelDB)
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

func TestDeprovisionDeletesVaultCredentials(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(testVaultDel, "org1-vault-del", 1)

	v := newFakeVault()
	svc.SetVault(v)

	// Seed every credential path the provisioner writes during a real run.
	// Paths are project-scoped only — no org dimension.
	prefix := "projects/vault-del/credentials/"
	roles := []string{"admin", "auth_admin", "excalibase_app", "cdc_watcher"}
	for _, r := range roles {
		if err := v.Put(prefix+r, map[string]string{"password": "secret"}); err != nil {
			t.Fatalf("seed %s: %v", r, err)
		}
	}
	// A path outside the project — must NOT be deleted.
	v.Put("projects/other-proj/credentials/admin", map[string]string{"password": "untouched"})

	store.Create(&domain.DatabaseInstance{
		ProjectID: testVaultDel,
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Namespace: "org1-vault-del",
		Status:    "ACTIVE",
	})

	if err := svc.Deprovision(context.Background(), testVaultDel); err != nil {
		t.Fatalf(testDeprovisionFmt, err)
	}

	for _, r := range roles {
		p := prefix + r
		if _, leaked := v.data[p]; leaked {
			t.Errorf("vault path %s should be deleted, still present", p)
		}
	}
	if _, ok := v.data["projects/other-proj/credentials/admin"]; !ok {
		t.Error("unrelated project's credentials should not be deleted")
	}
}

// Vault paths must NOT be org-scoped. If a regression reintroduces the old
// scheme, this test catches it: a credential filed under the legacy path
// projects/{orgSlug}/{projectId}/... must NOT be reachable from Deprovision
// because the canonical path is projects/{projectId}/...
func TestDeprovisionDoesNotSweepLegacyOrgScopedPaths(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(testLegacyProj, "org9-legacy-proj", 1)

	v := newFakeVault()
	svc.SetVault(v)

	// Legacy path layout — should never have been written under the new
	// scheme, but if a stale operator put one there it must be left alone
	// (deletion would only happen via explicit migration, not Deprovision).
	legacy := "projects/default/legacy-proj/credentials/excalibase_app"
	v.Put(legacy, map[string]string{"password": "stale"})
	// Canonical path — must be deleted.
	canonical := "projects/legacy-proj/credentials/excalibase_app"
	v.Put(canonical, map[string]string{"password": "current"})

	store.Create(&domain.DatabaseInstance{
		ProjectID: testLegacyProj,
		OrgID:     "org9",
		DBType:    domain.PostgreSQL,
		Status:    "ACTIVE",
	})

	if err := svc.Deprovision(context.Background(), testLegacyProj); err != nil {
		t.Fatalf(testDeprovisionFmt, err)
	}
	if _, leaked := v.data[canonical]; leaked {
		t.Errorf("canonical path %s should be deleted", canonical)
	}
	// Legacy path is intentionally not swept by Deprovision.
}

// Vault outage during list must log+continue, not strand the instance row.
func TestDeprovisionContinuesWhenVaultListFails(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(testVaultErr, "org1-vault-err", 1)

	v := &errVault{err: errors.New("vault outage")}
	svc.SetVault(v)

	store.Create(&domain.DatabaseInstance{
		ProjectID: testVaultErr,
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Namespace: "org1-vault-err",
		Status:    "ACTIVE",
	})

	if err := svc.Deprovision(context.Background(), testVaultErr); err != nil {
		t.Fatalf("Deprovision must succeed despite vault list failure: %v", err)
	}
	if inst, _ := store.FindByProjectID(testVaultErr); inst != nil {
		t.Error("instance row should still be deleted")
	}
}

// Sealed vault must skip the cleanup branch entirely (no calls into vault).
func TestDeprovisionSkipsVaultWhenSealed(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(testSealedDel, "org1-sealed-del", 1)

	v := &sealedVault{}
	svc.SetVault(v)

	store.Create(&domain.DatabaseInstance{
		ProjectID: testSealedDel,
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Namespace: "org1-sealed-del",
		Status:    "ACTIVE",
	})

	if err := svc.Deprovision(context.Background(), testSealedDel); err != nil {
		t.Fatalf(testDeprovisionFmt, err)
	}
	if v.listCalls != 0 || v.deleteCalls != 0 || v.deletePrefixCalls != 0 {
		t.Errorf("sealed vault must not be called: list=%d delete=%d deletePrefix=%d",
			v.listCalls, v.deleteCalls, v.deletePrefixCalls)
	}
}

func TestDeprovisionDeletionProtection(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	protected := true
	store.Create(&domain.DatabaseInstance{
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
	store.Create(&domain.DatabaseInstance{
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
	store.Create(&domain.DatabaseInstance{ProjectID: "dp-db", Status: "ACTIVE"})

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
	store.Create(&domain.DatabaseInstance{ProjectID: "a", Status: "ACTIVE"})
	store.Create(&domain.DatabaseInstance{ProjectID: "b", Status: "ACTIVE"})

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
		ProjectName: testCoolApp,
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf(testProvisionFmt, err)
	}
	if !projectRefPattern.MatchString(resp.ProjectID) {
		t.Errorf("ProjectID: got %q, want match %s", resp.ProjectID, projectRefPattern)
	}
	if resp.ProjectID == testCoolApp {
		t.Errorf("ProjectID must be generated, not the display name")
	}

	inst, _ := store.FindByProjectID(resp.ProjectID)
	if inst == nil {
		t.Fatalf("instance not persisted under generated ID %q", resp.ProjectID)
	}
	if inst.ProjectName != testCoolApp {
		t.Errorf("inst.ProjectName: got %q, want %q", inst.ProjectName, testCoolApp)
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
		t.Fatalf(testProvisionFmt, err)
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
func (f *fakeVault) DeletePrefix(prefix string) (int, error) {
	if prefix == "" {
		return 0, errors.New("empty prefix")
	}
	n := 0
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			f.deletes = append(f.deletes, k)
			delete(f.data, k)
			n++
		}
	}
	return n, nil
}
func (f *fakeVault) List(prefix string) ([]string, error) {
	out := []string{}
	for k := range f.data {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}
func (f *fakeVault) Sealed() bool                  { return false }
func (f *fakeVault) GetPublicKey() (string, error) { return "", nil }

// errVault returns a fixed error from List — used to verify Deprovision
// logs+continues on vault outage rather than stranding the instance row.
type errVault struct{ err error }

func (e *errVault) Get(string) (map[string]string, error) { return nil, e.err }
func (e *errVault) Put(string, map[string]string) error   { return e.err }
func (e *errVault) Delete(string) error                   { return e.err }
func (e *errVault) DeletePrefix(string) (int, error)      { return 0, e.err }
func (e *errVault) List(string) ([]string, error)         { return nil, e.err }
func (e *errVault) Sealed() bool                          { return false }
func (e *errVault) GetPublicKey() (string, error)         { return "", e.err }

// sealedVault reports Sealed=true and counts any other call so the test
// can assert nothing else was invoked.
type sealedVault struct {
	listCalls         int
	deleteCalls       int
	deletePrefixCalls int
}

func (s *sealedVault) Get(string) (map[string]string, error) { return nil, nil }
func (s *sealedVault) Put(string, map[string]string) error   { return nil }
func (s *sealedVault) Delete(string) error                   { s.deleteCalls++; return nil }
func (s *sealedVault) DeletePrefix(string) (int, error)      { s.deletePrefixCalls++; return 0, nil }
func (s *sealedVault) List(string) ([]string, error)         { s.listCalls++; return nil, nil }
func (s *sealedVault) Sealed() bool                          { return true }
func (s *sealedVault) GetPublicKey() (string, error)         { return "", nil }

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
		t.Fatal(testNotPersisted)
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
		t.Fatalf(testUnexpErrFmt, err)
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
		t.Fatal(testNotPersisted)
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
		t.Fatalf(testUnexpErrFmt, err)
	}
	if resp.Status != "FAILED" {
		t.Errorf("status: got %s, want FAILED", resp.Status)
	}
	if resp.FailureStage != domain.StageRoleCreation {
		t.Errorf("FailureStage: got %s, want ROLE_CREATION", resp.FailureStage)
	}

	// Admin vault entry must be rolled back (created then deleted)
	adminPath := "projects/" + resp.ProjectID + "/credentials/admin"
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
		t.Fatal(testNotPersisted)
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
		t.Fatalf(testUnexpErrFmt, err)
	}
	if resp.FailureStage != domain.StageNamespaceCreation {
		t.Errorf("FailureStage: got %s", resp.FailureStage)
	}
	inst, _ := store.FindByProjectID(resp.ProjectID)
	if inst == nil {
		t.Fatal(testNotPersisted)
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
	store.Create(&domain.DatabaseInstance{ProjectID: "a", OwnerID: testUser1, Status: "ACTIVE"})
	store.Create(&domain.DatabaseInstance{ProjectID: "b", OwnerID: testUser1, Status: "ACTIVE"})
	store.Create(&domain.DatabaseInstance{ProjectID: "c", OwnerID: "user-2", Status: "ACTIVE"})

	owned, err := svc.GetInstancesByOwner(testUser1)
	if err != nil {
		t.Fatalf("GetInstancesByOwner: %v", err)
	}
	if len(owned) != 2 {
		t.Errorf("expected 2, got %d", len(owned))
	}
	for _, inst := range owned {
		if inst.OwnerID != testUser1 {
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
