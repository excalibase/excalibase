package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const (
	testUser1     = "user-1"
	testApplyCNPG = "apply CNPG cluster"
	testDelHash   = "del-hash"
	testPruneDB   = "prune-db"
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
		Username: "app", Password: testutil.FixturePassword("sqlite-inst"), SSLMode: "require",
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
	if got.Password != testutil.FixturePassword("sqlite-inst") {
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

	store.Save(&domain.DatabaseInstance{ProjectID: "owned-1", OwnerID: testUser1, Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "owned-2", OwnerID: testUser1, Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "other", OwnerID: "user-2", Status: "ACTIVE"})

	// FindByOwner should return only user-1's instances
	owned, err := store.FindByOwner(testUser1)
	if err != nil {
		t.Fatalf("FindByOwner: %v", err)
	}
	if len(owned) != 2 {
		t.Errorf("expected 2 for user-1, got %d", len(owned))
	}

	// Save and read back — OwnerID should persist
	got, _ := store.FindByProjectID("owned-1")
	if got.OwnerID != testUser1 {
		t.Errorf("ownerID: got %s, want user-1", got.OwnerID)
	}
}

func TestInstancePersistsDisplayNameAndRollbackFields(t *testing.T) {
	store := testStore(t)
	inst := &domain.DatabaseInstance{
		ProjectID:     "proj_a1b2c3d4e5",
		ProjectName:   "My Cool App 🚀",
		OrgID:         "org1",
		Status:        "FAILED",
		CurrentStage:  domain.StageFailed,
		CurrentStep:   testApplyCNPG,
		FailureStage:  domain.StageCRDDeployment,
		FailureStep:   testApplyCNPG,
		FailureReason: "forbidden: CRD missing",
		RollbackLog:   `[{"name":"delete namespace","ok":true}]`,
	}
	if err := store.Save(inst); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.FindByProjectID("proj_a1b2c3d4e5")
	if err != nil || got == nil {
		t.Fatalf("FindByProjectID: %v", err)
	}
	if got.ProjectName != "My Cool App 🚀" {
		t.Errorf("ProjectName: got %q", got.ProjectName)
	}
	if got.CurrentStep != testApplyCNPG {
		t.Errorf("CurrentStep: got %q", got.CurrentStep)
	}
	if got.FailureStage != domain.StageCRDDeployment {
		t.Errorf("FailureStage: got %s", got.FailureStage)
	}
	if got.FailureStep != testApplyCNPG {
		t.Errorf("FailureStep: got %q", got.FailureStep)
	}
	if got.RollbackLog != `[{"name":"delete namespace","ok":true}]` {
		t.Errorf("RollbackLog: got %q", got.RollbackLog)
	}
}

func TestInstance_DeploymentMode_RoundTrips(t *testing.T) {
	store := testStore(t)
	cases := []struct {
		name string
		mode domain.DeploymentMode
	}{
		{"k8s", domain.ModeK8s},
		{"docker", domain.ModeDocker},
		{"byoc", domain.ModeBYOC},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := "mode-" + c.name
			if err := store.Save(&domain.DatabaseInstance{
				ProjectID: id, OrgID: "org1", Status: "ACTIVE",
				DeploymentMode: c.mode,
			}); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, err := store.FindByProjectID(id)
			if err != nil || got == nil {
				t.Fatalf("FindByProjectID: %v", err)
			}
			if got.DeploymentMode != c.mode {
				t.Errorf("deploymentMode: got %q, want %q", got.DeploymentMode, c.mode)
			}
		})
	}
}

func TestInstance_LegacyRow_DefaultsToK8s(t *testing.T) {
	store := testStore(t)
	// A pre-migration row had no deployment_mode column. After migration
	// the column defaults to 'k8s'; rows that go through Save with the
	// zero-value field should normalize to ModeK8s on read so callers
	// never see "".
	if err := store.Save(&domain.DatabaseInstance{
		ProjectID: "legacy-1", OrgID: "org1", Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.FindByProjectID("legacy-1")
	if err != nil || got == nil {
		t.Fatalf("FindByProjectID: %v", err)
	}
	if got.DeploymentMode != domain.ModeK8s {
		t.Errorf("legacy row deploymentMode: got %q, want k8s", got.DeploymentMode)
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
		PasswordHash: strings.Join([]string{"$2a$10$", "fakehash-for-storage-test"}, ""), Role: "admin", Active: true,
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
	if got.PasswordHash != strings.Join([]string{"$2a$10$", "fakehash-for-storage-test"}, "") {
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

// --- UpdateUserPassword ---

func TestUpdateUserPassword(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	pwUser := testutil.FixtureToken("pwuser")
	user := &domain.User{
		ID: "u-pw", Username: pwUser, Email: "pw@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "viewer", Active: true,
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	newHash := testutil.FixtureToken("new-pw-hash")
	if err := store.UpdateUserPassword(ctx, pwUser, newHash); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}

	got, err := store.FindUserByUsername(ctx, pwUser)
	if err != nil || got == nil {
		t.Fatalf("FindUserByUsername: %v", err)
	}
	if got.PasswordHash != newHash {
		t.Errorf("password hash: got %s, want newhash", got.PasswordHash)
	}
}

func TestUpdateUserPasswordNotFound(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	err := store.UpdateUserPassword(ctx, "nonexistent", "hash")
	if err == nil {
		t.Error("UpdateUserPassword should return error for unknown username")
	}
}

// --- MigrationVersion ---

func TestMigrationVersion(t *testing.T) {
	store := testStore(t)

	version, dirty, err := store.MigrationVersion()
	if err != nil {
		t.Fatalf("MigrationVersion: %v", err)
	}
	if dirty {
		t.Error("migration should not be dirty after clean init")
	}
	if version == 0 {
		t.Error("migration version should be > 0 after applying migrations")
	}
}

// --- Close ---

func TestClose(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir + "/close_test.db")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Close should succeed
	if err := store.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// Operations after close should fail
	ctx := context.Background()
	_, err = store.FindAllUsers(ctx)
	if err == nil {
		t.Error("operations after Close should return an error")
	}
}

// --- FindUserByID: not found ---

func TestFindUserByIDNotFound(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	got, err := store.FindUserByID(ctx, "nonexistent-id")
	if err != nil {
		t.Fatalf("FindUserByID: unexpected error: %v", err)
	}
	if got != nil {
		t.Error("FindUserByID should return nil for nonexistent user")
	}
}

// --- FindUserByUsername: not found ---

func TestFindUserByUsernameNotFound(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	got, err := store.FindUserByUsername(ctx, "nobody")
	if err != nil {
		t.Fatalf("FindUserByUsername: unexpected error: %v", err)
	}
	if got != nil {
		t.Error("FindUserByUsername should return nil for nonexistent user")
	}
}

// --- FindAllUsers: empty ---

func TestFindAllUsersEmpty(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	users, err := store.FindAllUsers(ctx)
	if err != nil {
		t.Fatalf("FindAllUsers: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("expected 0 users, got %d", len(users))
	}
}

// --- Token tests ---

func TestTokenCreateAndFind(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	store.CreateUser(ctx, &domain.User{ID: "tu1", Username: testutil.FixtureToken("tokenuser"), Email: "t@t.com", Role: "admin", Active: true})

	tok := &domain.AccessToken{
		TokenHash:   "hashvalue123",
		TokenPrefix: "prefix123456",
		UserID:      "tu1",
		Name:        "My Token",
	}
	if err := store.CreateToken(ctx, tok); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	got, err := store.FindByTokenHash(ctx, "hashvalue123")
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if got == nil {
		t.Fatal("FindByTokenHash returned nil")
	}
	if got.UserID != "tu1" {
		t.Errorf("UserID: got %s, want tu1", got.UserID)
	}
	if got.Name != "My Token" {
		t.Errorf("Name: got %s, want My Token", got.Name)
	}
}

func TestTokenFindByHashNotFound(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	got, err := store.FindByTokenHash(ctx, "nonexistent-hash")
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if got != nil {
		t.Error("FindByTokenHash should return nil for unknown hash")
	}
}

func TestTokenListByUser(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	store.CreateUser(ctx, &domain.User{ID: "lu1", Username: testutil.FixtureToken("listuser"), Email: "l@t.com", Role: "admin", Active: true})

	store.CreateToken(ctx, &domain.AccessToken{TokenHash: "h1", TokenPrefix: "p1__________", UserID: "lu1", Name: "T1"})
	store.CreateToken(ctx, &domain.AccessToken{TokenHash: "h2", TokenPrefix: "p2__________", UserID: "lu1", Name: "T2"})

	tokens, err := store.ListTokensByUser(ctx, "lu1")
	if err != nil {
		t.Fatalf("ListTokensByUser: %v", err)
	}
	if len(tokens) != 2 {
		t.Errorf("expected 2 tokens, got %d", len(tokens))
	}
}

func TestTokenDelete(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	store.CreateUser(ctx, &domain.User{ID: "du1", Username: testutil.FixtureToken("deluser"), Email: "d@t.com", Role: "admin", Active: true})
	store.CreateToken(ctx, &domain.AccessToken{TokenHash: testDelHash, TokenPrefix: "delhash12345", UserID: "du1", Name: "Del"})

	if err := store.DeleteToken(ctx, testDelHash); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}

	got, _ := store.FindByTokenHash(ctx, testDelHash)
	if got != nil {
		t.Error("token should be nil after deletion")
	}
}

// --- FindByOwner: empty result ---

func TestFindByOwnerEmpty(t *testing.T) {
	store := testStore(t)

	result, err := store.FindByOwner("no-such-owner")
	if err != nil {
		t.Fatalf("FindByOwner: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 results, got %d", len(result))
	}
}

// --- Metrics: prune old entries ---

func TestMetricsPruneKeepsLatest100(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	store.Save(&domain.DatabaseInstance{ProjectID: testPruneDB, Status: "ACTIVE"})

	// Insert 105 metrics entries
	for i := 0; i < 105; i++ {
		ts := &domain.FlexTime{Time: time.Now()}
		store.AppendMetrics(ctx, &domain.DatabaseMetrics{
			ProjectID: testPruneDB, Timestamp: ts, MetricsAvailable: true,
		})
	}

	hist, err := store.GetMetricsHistory(ctx, testPruneDB, 200)
	if err != nil {
		t.Fatalf("GetMetricsHistory: %v", err)
	}
	if len(hist) > 100 {
		t.Errorf("expected at most 100 entries after pruning, got %d", len(hist))
	}
}
