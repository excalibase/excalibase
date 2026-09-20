package storage

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const (
	testPersistDB = "persist-db"
	testDelDB     = "del-db"
	testOwner1    = "owner-1"
)

func TestFileSystemStoreSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("NewFileSystemStore: %v", err)
	}

	now := &domain.FlexTime{Time: time.Now()}
	port := 5432
	inst := &domain.DatabaseInstance{
		ProjectID:    "test-db",
		OrgID:        "org1",
		DBType:       domain.PostgreSQL,
		Tier:         domain.Standard,
		Namespace:    "org1-test-db",
		Host:         "test-db-postgres-rw.org1-test-db.svc.cluster.local",
		Port:         &port,
		DatabaseName: "app",
		Username:     "app",
		Password:     testutil.FixturePassword("fs-store"),
		Status:       "ACTIVE",
		CurrentStage: domain.StageCompleted,
		CreatedAt:    now,
	}

	if err := store.Create(inst); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Read back
	got, err := store.FindByProjectID("test-db")
	if err != nil {
		t.Fatalf("FindByProjectID: %v", err)
	}
	if got == nil {
		t.Fatal("FindByProjectID returned nil")
	}
	if got.Password != testutil.FixturePassword("fs-store") {
		t.Errorf("password: got %s, want secretpassword123", got.Password)
	}
	if got.Host != "test-db-postgres-rw.org1-test-db.svc.cluster.local" {
		t.Errorf("host: got %s", got.Host)
	}
}

func TestFileSystemStoreReloadFromDisk(t *testing.T) {
	dir := t.TempDir()
	store1, _ := NewFileSystemStore(dir)

	port := 5432
	store1.Create(&domain.DatabaseInstance{
		ProjectID: testPersistDB,
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Tier:      domain.Free,
		Namespace: "org1-persist-db",
		Host:      "host.local",
		Port:      &port,
		Username:  "user",
		Password:  testutil.FixturePassword(testPersistDB),
		Status:    "ACTIVE",
	})

	// Create new store from same dir (simulates restart)
	store2, err := NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	got, _ := store2.FindByProjectID(testPersistDB)
	if got == nil {
		t.Fatal("instance not found after reload")
	}
	if got.Password != testutil.FixturePassword(testPersistDB) {
		t.Errorf("password after reload: got %s, want pass123", got.Password)
	}
}

func TestFileSystemStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileSystemStore(dir)

	store.Create(&domain.DatabaseInstance{
		ProjectID: testDelDB,
		OrgID:     "org1",
		Status:    "ACTIVE",
	})

	if err := store.Delete(testDelDB); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := store.FindByProjectID(testDelDB)
	if got != nil {
		t.Error("instance should be nil after delete")
	}
}

func TestFileSystemStoreFindAll(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileSystemStore(dir)

	store.Create(&domain.DatabaseInstance{ProjectID: "db1", Status: "ACTIVE"})
	store.Create(&domain.DatabaseInstance{ProjectID: "db2", Status: "ACTIVE"})

	all, _ := store.FindAll()
	if len(all) != 2 {
		t.Errorf("FindAll: got %d, want 2", len(all))
	}
}

func TestFileSystemStore_LegacyRow_DefaultsToK8s(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileSystemStore(dir)
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "legacy", OrgID: "org1", Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fresh, err := NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, _ := fresh.FindByProjectID("legacy")
	if got == nil {
		t.Fatal("not found")
	}
	if got.DeploymentMode != domain.ModeK8s {
		t.Errorf("legacy row deploymentMode: got %q, want k8s", got.DeploymentMode)
	}
}

func TestFileSystemStore_DeploymentMode_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileSystemStore(dir)
	cases := []domain.DeploymentMode{domain.ModeK8s, domain.ModeDocker}
	for _, mode := range cases {
		t.Run(string(mode), func(t *testing.T) {
			id := "mode-" + string(mode)
			if err := store.Create(&domain.DatabaseInstance{
				ProjectID: id, OrgID: "org1", Status: "ACTIVE",
				DeploymentMode: mode,
			}); err != nil {
				t.Fatalf("Save: %v", err)
			}
			// Reload from disk to confirm persistence (not just cache).
			fresh, err := NewFileSystemStore(dir)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			got, _ := fresh.FindByProjectID(id)
			if got == nil {
				t.Fatalf("not found after reload")
			}
			if got.DeploymentMode != mode {
				t.Errorf("deploymentMode: got %q, want %q", got.DeploymentMode, mode)
			}
		})
	}
}

func TestFileSystemStoreNotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileSystemStore(dir)

	got, err := store.FindByProjectID("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent project")
	}
}

func TestFileSystemStoreFindByOwner(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("NewFileSystemStore: %v", err)
	}

	store.Create(&domain.DatabaseInstance{ProjectID: "db-a", OwnerID: testOwner1, Status: "ACTIVE"})
	store.Create(&domain.DatabaseInstance{ProjectID: "db-b", OwnerID: testOwner1, Status: "ACTIVE"})
	store.Create(&domain.DatabaseInstance{ProjectID: "db-c", OwnerID: "owner-2", Status: "ACTIVE"})

	owned, err := store.FindByOwner(testOwner1)
	if err != nil {
		t.Fatalf("FindByOwner: %v", err)
	}
	if len(owned) != 2 {
		t.Errorf("FindByOwner(owner-1): got %d, want 2", len(owned))
	}
	for _, inst := range owned {
		if inst.OwnerID != testOwner1 {
			t.Errorf("FindByOwner returned wrong owner: %s", inst.OwnerID)
		}
	}
}

func TestFileSystemStoreFindByOwnerEmpty(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileSystemStore(dir)

	store.Create(&domain.DatabaseInstance{ProjectID: "db-x", OwnerID: "other-owner", Status: "ACTIVE"})

	result, err := store.FindByOwner("no-such-owner")
	if err != nil {
		t.Fatalf("FindByOwner: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("FindByOwner: expected 0, got %d", len(result))
	}
}

func TestFileSystemStoreFindByOwnerPersistedAcrossReload(t *testing.T) {
	dir := t.TempDir()
	store1, _ := NewFileSystemStore(dir)

	store1.Create(&domain.DatabaseInstance{ProjectID: "persist-a", OwnerID: "owner-x", Status: "ACTIVE"})
	store1.Create(&domain.DatabaseInstance{ProjectID: "persist-b", OwnerID: "owner-y", Status: "ACTIVE"})

	// Simulate restart
	store2, err := NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	owned, err := store2.FindByOwner("owner-x")
	if err != nil {
		t.Fatalf("FindByOwner after reload: %v", err)
	}
	if len(owned) != 1 {
		t.Errorf("expected 1 for owner-x after reload, got %d", len(owned))
	}
	if owned[0].ProjectID != "persist-a" {
		t.Errorf("ProjectID: got %s, want persist-a", owned[0].ProjectID)
	}
}

func storedProject(projectID, orgID string) *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID: projectID, ProjectName: projectID, OrgID: orgID,
		DBType: domain.PostgreSQL, Tier: domain.Free, Namespace: orgID + "-" + projectID,
		Host: projectID + "-rw", DatabaseName: "app", Username: projectID + "_admin",
		Password: testutil.FixturePassword("fs-" + projectID), Status: "ACTIVE",
	}
}

// A project id is claimed by whoever registered it: a second Create is a
// conflict, and the stored row is untouched (EXC-415).
func TestFileSystemStoreCreateRejectsAnExistingProjectID(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSystemStore: %v", err)
	}
	victim := storedProject("proj-victim01", "org-victim")
	if err := store.Create(victim); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.Create(storedProject("proj-victim01", "org-attacker")); err != ErrProjectExists {
		t.Fatalf("Create on a taken id: got %v, want ErrProjectExists", err)
	}

	got, _ := store.FindByProjectID("proj-victim01")
	if got.OrgID != "org-victim" || got.Host != victim.Host {
		t.Errorf("stored row was rewritten: org=%q host=%q", got.OrgID, got.Host)
	}
}

// Update persists changes without ever moving the project to another org.
func TestFileSystemStoreUpdateKeepsTheOwningOrg(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSystemStore: %v", err)
	}
	if err := store.Create(storedProject("proj-victim02", "org-victim")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	moved := storedProject("proj-victim02", "org-attacker")
	moved.Status = "PAUSED"
	moved.Host = "moved-rw"
	if err := store.Update(moved); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := store.FindByProjectID("proj-victim02")
	if got.OrgID != "org-victim" {
		t.Errorf("org changed by Update: got %q", got.OrgID)
	}
	if got.Status != "PAUSED" || got.Host != "moved-rw" {
		t.Errorf("Update must persist mutable fields: status=%q host=%q", got.Status, got.Host)
	}
}

// Update never inserts: an absent row means the caller holds a stale view.
func TestFileSystemStoreUpdateMissingRowIsAnError(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSystemStore: %v", err)
	}
	if err := store.Update(storedProject("proj-absent01", "org-x")); err != ErrProjectNotFound {
		t.Fatalf("Update on a missing row: got %v, want ErrProjectNotFound", err)
	}
	if got, _ := store.FindByProjectID("proj-absent01"); got != nil {
		t.Error("a failed Update must not create the row")
	}
}
