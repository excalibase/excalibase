package storage

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
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
		Password:     "secretpassword123",
		Status:       "ACTIVE",
		CurrentStage: domain.StageCompleted,
		CreatedAt:    now,
	}

	if err := store.Save(inst); err != nil {
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
	if got.Password != "secretpassword123" {
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
	store1.Save(&domain.DatabaseInstance{
		ProjectID: "persist-db",
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Tier:      domain.Free,
		Namespace: "org1-persist-db",
		Host:      "host.local",
		Port:      &port,
		Username:  "user",
		Password:  "pass123",
		Status:    "ACTIVE",
	})

	// Create new store from same dir (simulates restart)
	store2, err := NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	got, _ := store2.FindByProjectID("persist-db")
	if got == nil {
		t.Fatal("instance not found after reload")
	}
	if got.Password != "pass123" {
		t.Errorf("password after reload: got %s, want pass123", got.Password)
	}
}

func TestFileSystemStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileSystemStore(dir)

	store.Save(&domain.DatabaseInstance{
		ProjectID: "del-db",
		OrgID:     "org1",
		Status:    "ACTIVE",
	})

	if err := store.Delete("del-db"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := store.FindByProjectID("del-db")
	if got != nil {
		t.Error("instance should be nil after delete")
	}
}

func TestFileSystemStoreFindAll(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileSystemStore(dir)

	store.Save(&domain.DatabaseInstance{ProjectID: "db1", Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "db2", Status: "ACTIVE"})

	all, _ := store.FindAll()
	if len(all) != 2 {
		t.Errorf("FindAll: got %d, want 2", len(all))
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

	store.Save(&domain.DatabaseInstance{ProjectID: "db-a", OwnerID: "owner-1", Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "db-b", OwnerID: "owner-1", Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "db-c", OwnerID: "owner-2", Status: "ACTIVE"})

	owned, err := store.FindByOwner("owner-1")
	if err != nil {
		t.Fatalf("FindByOwner: %v", err)
	}
	if len(owned) != 2 {
		t.Errorf("FindByOwner(owner-1): got %d, want 2", len(owned))
	}
	for _, inst := range owned {
		if inst.OwnerID != "owner-1" {
			t.Errorf("FindByOwner returned wrong owner: %s", inst.OwnerID)
		}
	}
}

func TestFileSystemStoreFindByOwnerEmpty(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileSystemStore(dir)

	store.Save(&domain.DatabaseInstance{ProjectID: "db-x", OwnerID: "other-owner", Status: "ACTIVE"})

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

	store1.Save(&domain.DatabaseInstance{ProjectID: "persist-a", OwnerID: "owner-x", Status: "ACTIVE"})
	store1.Save(&domain.DatabaseInstance{ProjectID: "persist-b", OwnerID: "owner-y", Status: "ACTIVE"})

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
