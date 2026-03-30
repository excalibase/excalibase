package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := New(dir + "/test.db")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// --- Instance tests ---

func TestInstanceSaveAndFind(t *testing.T) {
	store := testStore(t)
	port := 5432
	now := time.Now()
	ft := &domain.FlexTime{Time: now}

	inst := &domain.DatabaseInstance{
		ProjectID: "test-db", OrgID: "org1", DBType: domain.PostgreSQL,
		Tier: domain.Standard, Namespace: "org1-test-db",
		Host: "host.local", Port: &port, DatabaseName: "app",
		Username: "app", Password: "secret123", SSLMode: "require",
		Status: "ACTIVE", CurrentStage: domain.StageCompleted,
		CreatedAt: ft,
	}

	if err := store.Save(inst); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.FindByProjectID("test-db")
	if err != nil {
		t.Fatalf("FindByProjectID: %v", err)
	}
	if got == nil {
		t.Fatal("not found")
	}
	if got.Host != "host.local" {
		t.Errorf("host: got %s", got.Host)
	}
	if got.Password != "secret123" {
		t.Errorf("password: got %s", got.Password)
	}
	if got.Status != "ACTIVE" {
		t.Errorf("status: got %s", got.Status)
	}
}

func TestInstanceFindAll(t *testing.T) {
	store := testStore(t)
	store.Save(&domain.DatabaseInstance{ProjectID: "a", OrgID: "org", Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "b", OrgID: "org", Status: "ACTIVE"})

	all, _ := store.FindAll()
	if len(all) != 2 {
		t.Errorf("expected 2, got %d", len(all))
	}
}

func TestInstanceDelete(t *testing.T) {
	store := testStore(t)
	store.Save(&domain.DatabaseInstance{ProjectID: "del", Status: "ACTIVE"})
	store.Delete("del")

	got, _ := store.FindByProjectID("del")
	if got != nil {
		t.Error("should be nil after delete")
	}
}

func TestInstanceUpdate(t *testing.T) {
	store := testStore(t)
	store.Save(&domain.DatabaseInstance{ProjectID: "upd", Status: "PROVISIONING"})
	store.Save(&domain.DatabaseInstance{ProjectID: "upd", Status: "ACTIVE"})

	got, _ := store.FindByProjectID("upd")
	if got.Status != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", got.Status)
	}
}

func TestInstanceOwnerID(t *testing.T) {
	store := testStore(t)

	store.Save(&domain.DatabaseInstance{ProjectID: "owned-1", OwnerID: "user-1", Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "owned-2", OwnerID: "user-1", Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "other", OwnerID: "user-2", Status: "ACTIVE"})

	// FindByOwner should return only user-1's instances
	owned, err := store.FindByOwner("user-1")
	if err != nil {
		t.Fatalf("FindByOwner: %v", err)
	}
	if len(owned) != 2 {
		t.Errorf("expected 2 for user-1, got %d", len(owned))
	}

	// Save and read back — OwnerID should persist
	got, _ := store.FindByProjectID("owned-1")
	if got.OwnerID != "user-1" {
		t.Errorf("ownerID: got %s, want user-1", got.OwnerID)
	}
}

func TestInstanceNotFound(t *testing.T) {
	store := testStore(t)
	got, _ := store.FindByProjectID("nope")
	if got != nil {
		t.Error("expected nil")
	}
}

// --- User tests ---

func TestUserCreateAndFind(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	user := &domain.User{
		ID: "u1", Username: "admin", Email: "admin@test.com",
		PasswordHash: "$2a$10$hash", Role: "admin", Active: true,
	}

	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := store.FindUserByID(ctx, "u1")
	if err != nil || got == nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.Username != "admin" {
		t.Errorf("username: got %s", got.Username)
	}
	if got.PasswordHash != "$2a$10$hash" {
		t.Errorf("password hash not stored")
	}

	byName, _ := store.FindUserByUsername(ctx, "admin")
	if byName == nil {
		t.Fatal("FindByUsername returned nil")
	}
}

func TestUserFindAll(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	store.CreateUser(ctx, &domain.User{ID: "u1", Username: "a", Email: "a@t.com", Role: "admin", Active: true})
	store.CreateUser(ctx, &domain.User{ID: "u2", Username: "b", Email: "b@t.com", Role: "viewer", Active: true})

	all, _ := store.FindAllUsers(ctx)
	if len(all) != 2 {
		t.Errorf("expected 2, got %d", len(all))
	}
}

func TestUserDelete(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	store.CreateUser(ctx, &domain.User{ID: "del", Username: "del", Email: "d@t.com", Role: "viewer", Active: true})
	store.DeleteUser(ctx, "del")

	got, _ := store.FindUserByID(ctx, "del")
	if got != nil {
		t.Error("should be nil")
	}
}

// --- Metrics tests ---

func TestMetricsAppendAndHistory(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// Need an instance first (FK)
	store.Save(&domain.DatabaseInstance{ProjectID: "m-db", Status: "ACTIVE"})

	now := &domain.FlexTime{Time: time.Now()}
	active := 5
	maxConn := 100
	store.AppendMetrics(ctx, &domain.DatabaseMetrics{
		ProjectID: "m-db", Timestamp: now, MetricsAvailable: true,
		HealthStatus: "HEALTHY", ActiveConnections: &active, MaxConnections: &maxConn,
	})

	hist, _ := store.GetMetricsHistory(ctx, "m-db", 10)
	if len(hist) != 1 {
		t.Fatalf("expected 1, got %d", len(hist))
	}
	if *hist[0].ActiveConnections != 5 {
		t.Errorf("connections: got %d", *hist[0].ActiveConnections)
	}
}

// --- Alert tests ---

func TestAlertSaveAndQuery(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	now := &domain.FlexTime{Time: time.Now()}
	store.SaveAlert(ctx, &domain.Alert{
		ID: "a1", ProjectID: "db1", Severity: "WARNING",
		Message: "CPU high", Timestamp: now,
	})

	active, _ := store.GetActiveAlerts(ctx)
	if len(active) != 1 {
		t.Errorf("expected 1 active, got %d", len(active))
	}

	forProject, _ := store.GetActiveAlertsForProject(ctx, "db1")
	if len(forProject) != 1 {
		t.Errorf("expected 1 for project, got %d", len(forProject))
	}

	hist, _ := store.GetAlertHistory(ctx, 100)
	if len(hist) != 1 {
		t.Errorf("expected 1 in history, got %d", len(hist))
	}
}

// --- Audit log tests ---

func TestAuditLog(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	now := time.Now()
	store.LogAudit(ctx, &domain.AuditEntry{
		UserID: "u1", Action: "provision", Resource: "instance",
		ResourceID: "db1", Timestamp: &now,
	})

	entries, _ := store.QueryAudit(ctx, 10)
	if len(entries) != 1 {
		t.Errorf("expected 1, got %d", len(entries))
	}
	if entries[0].Action != "provision" {
		t.Errorf("action: got %s", entries[0].Action)
	}
}
