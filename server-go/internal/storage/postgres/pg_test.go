//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	testUser1      = "user-1"
	testApplyCNPG  = "apply CNPG cluster"
	testDelHash    = "del-hash"
	testAliceCorp  = "alice-corp"
	testRoleFmt    = "role: got %q"
	testMyProject  = "my-project"
	testOrgInv     = "org-inv"
	testNewGuyEmail = "newguy@test.com"
)


func testStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()

	containerPwd := testutil.FixturePassword("pg-container")
	pgContainer, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("platform_test"),
		postgres.WithUsername("platform"),
		postgres.WithPassword(containerPwd),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { pgContainer.Terminate(ctx) })

	host, _ := pgContainer.Host(ctx)
	port, _ := pgContainer.MappedPort(ctx, "5432/tcp")

	dsn := fmt.Sprintf("postgres://platform:%s@%s:%s/platform_test?sslmode=disable", containerPwd, host, port.Port())

	store, err := New(dsn)
	if err != nil {
		t.Fatalf("New postgres store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestInstance_DeploymentMode_RoundTrips covers the new column added by
// migration 000007. Without persistence here, BackupAdapter dispatch
// would always hit the zero-value mode for cloud-deployed instances.
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
		Username: "app", Password: testutil.FixturePassword("pg-inst"), SSLMode: "require",
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
	if got.Password != testutil.FixturePassword("pg-inst") {
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

	owned, err := store.FindByOwner(testUser1)
	if err != nil {
		t.Fatalf("FindByOwner: %v", err)
	}
	if len(owned) != 2 {
		t.Errorf("expected 2 for user-1, got %d", len(owned))
	}

	got, _ := store.FindByProjectID("owned-1")
	if got.OwnerID != testUser1 {
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

// --- User tests ---

func TestUserCreateAndFind(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	user := &domain.User{
		ID: "u1", Username: "admin", Email: "admin@test.com",
		PasswordHash: strings.Join([]string{"$2a$10$", "fakehash-pg-test"}, ""), Role: "admin", Active: true,
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
	if got.PasswordHash != strings.Join([]string{"$2a$10$", "fakehash-pg-test"}, "") {
		t.Errorf("password hash not stored")
	}

	byName, _ := store.FindUserByUsername(ctx, "admin")
	if byName == nil {
		t.Fatal("FindByUsername returned nil")
	}

	byEmail, _ := store.FindUserByEmail(ctx, "admin@test.com")
	if byEmail == nil {
		t.Fatal("FindByEmail returned nil")
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

	newHashPg := testutil.FixtureToken("new-pw-hash-pg")
	if err := store.UpdateUserPassword(ctx, pwUser, newHashPg); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}

	got, err := store.FindUserByUsername(ctx, pwUser)
	if err != nil || got == nil {
		t.Fatalf("FindUserByUsername: %v", err)
	}
	if got.PasswordHash != newHashPg {
		t.Errorf("password hash: got %s, want newhash", got.PasswordHash)
	}
}

func TestFindUserByIDNotFound(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	got, err := store.FindUserByID(ctx, "nonexistent-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("should return nil for nonexistent user")
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

// --- Metrics tests ---

func TestMetricsAppendAndHistory(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

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

// --- Org tests ---

func setupOrgTest(t *testing.T) (*Store, string) {
	t.Helper()
	store := testStore(t)
	ctx := context.Background()

	store.CreateUser(ctx, &domain.User{
		ID: testUser1, Username: testutil.FixtureToken("alice"), Email: "alice@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	})
	return store, testUser1
}

func TestCreateAndFindOrg(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	org := &domain.Org{
		ID: "org-1", Name: "Alice Corp", Slug: testAliceCorp,
		Tier: domain.Free, OwnerID: userID,
	}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	got, err := store.FindOrgByID(ctx, "org-1")
	if err != nil {
		t.Fatalf("FindOrgByID: %v", err)
	}
	if got.Name != "Alice Corp" {
		t.Errorf("name: got %q", got.Name)
	}
	if got.Slug != testAliceCorp {
		t.Errorf("slug: got %q", got.Slug)
	}

	got2, err := store.FindOrgBySlug(ctx, testAliceCorp)
	if err != nil {
		t.Fatalf("FindOrgBySlug: %v", err)
	}
	if got2.ID != "org-1" {
		t.Errorf("id: got %q", got2.ID)
	}
}

func TestFindOrgsByUser(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	store.CreateOrg(ctx, &domain.Org{ID: "org-a", Name: "Org A", Slug: "org-a", Tier: domain.Free, OwnerID: userID})
	store.CreateOrg(ctx, &domain.Org{ID: "org-b", Name: "Org B", Slug: "org-b", Tier: domain.Free, OwnerID: userID})
	store.AddOrgMember(ctx, &domain.OrgMember{OrgID: "org-a", UserID: userID, Role: domain.OrgRoleOwner})
	store.AddOrgMember(ctx, &domain.OrgMember{OrgID: "org-b", UserID: userID, Role: domain.OrgRoleOwner})

	orgs, err := store.FindOrgsByUser(ctx, userID)
	if err != nil {
		t.Fatalf("FindOrgsByUser: %v", err)
	}
	if len(orgs) != 2 {
		t.Errorf("expected 2 orgs, got %d", len(orgs))
	}
}

func TestOrgMemberCRUD(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	bobID := testutil.FixtureToken("bob-id")
	store.CreateUser(ctx, &domain.User{
		ID: bobID, Username: testutil.FixtureToken("bob"), Email: "bob@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	})

	store.CreateOrg(ctx, &domain.Org{ID: "org-m", Name: "Members", Slug: "members", Tier: domain.Free, OwnerID: userID})
	store.AddOrgMember(ctx, &domain.OrgMember{OrgID: "org-m", UserID: userID, Role: domain.OrgRoleOwner})

	if err := store.AddOrgMember(ctx, &domain.OrgMember{OrgID: "org-m", UserID: bobID, Role: domain.OrgRoleDeveloper}); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	members, err := store.ListOrgMembers(ctx, "org-m")
	if err != nil {
		t.Fatalf("ListOrgMembers: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("expected 2 members, got %d", len(members))
	}

	m, err := store.GetOrgMember(ctx, "org-m", bobID)
	if err != nil {
		t.Fatalf("GetOrgMember: %v", err)
	}
	if m.Role != domain.OrgRoleDeveloper {
		t.Errorf(testRoleFmt, m.Role)
	}
	if m.Email != "bob@test.com" {
		t.Errorf("email: got %q", m.Email)
	}

	store.UpdateOrgMemberRole(ctx, "org-m", bobID, domain.OrgRoleAdmin)
	m2, _ := store.GetOrgMember(ctx, "org-m", bobID)
	if m2.Role != domain.OrgRoleAdmin {
		t.Errorf("updated role: got %q", m2.Role)
	}

	store.RemoveOrgMember(ctx, "org-m", bobID)
	members2, _ := store.ListOrgMembers(ctx, "org-m")
	if len(members2) != 1 {
		t.Errorf("expected 1 member after remove, got %d", len(members2))
	}
}

func TestProjectMemberCRUD(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	charlieID := testutil.FixtureToken("charlie-id")
	charlieUser := testutil.FixtureToken("charlie")
	store.CreateUser(ctx, &domain.User{
		ID: charlieID, Username: charlieUser, Email: "charlie@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	})

	store.CreateOrg(ctx, &domain.Org{ID: "org-p", Name: "ProjOrg", Slug: "proj-org", Tier: domain.Free, OwnerID: userID})

	if err := store.AddProjectMember(ctx, &domain.ProjectMember{
		ProjectID: testMyProject, OrgID: "org-p", UserID: charlieID, Role: domain.ProjectRoleEditor,
	}); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	members, err := store.ListProjectMembers(ctx, testMyProject)
	if err != nil {
		t.Fatalf("ListProjectMembers: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("expected 1 member, got %d", len(members))
	}

	m, _ := store.GetProjectMember(ctx, testMyProject, charlieID)
	if m.Role != domain.ProjectRoleEditor {
		t.Errorf(testRoleFmt, m.Role)
	}
	if m.Username != charlieUser {
		t.Errorf("username: got %q", m.Username)
	}

	store.UpdateProjectMemberRole(ctx, testMyProject, charlieID, domain.ProjectRoleViewer)
	m2, _ := store.GetProjectMember(ctx, testMyProject, charlieID)
	if m2.Role != domain.ProjectRoleViewer {
		t.Errorf("updated role: got %q", m2.Role)
	}

	store.RemoveProjectMember(ctx, testMyProject, charlieID)
	members2, _ := store.ListProjectMembers(ctx, testMyProject)
	if len(members2) != 0 {
		t.Errorf("expected 0 members after remove, got %d", len(members2))
	}
}

func TestPendingInvites(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	store.CreateOrg(ctx, &domain.Org{ID: testOrgInv, Name: "Invites", Slug: "invites", Tier: domain.Free, OwnerID: userID})

	if err := store.CreatePendingInvite(ctx, &domain.PendingInvite{
		OrgID: testOrgInv, Email: testNewGuyEmail, Role: "developer", InvitedBy: userID,
	}); err != nil {
		t.Fatalf("CreatePendingInvite: %v", err)
	}

	invites, err := store.FindPendingInvitesByEmail(ctx, testNewGuyEmail)
	if err != nil {
		t.Fatalf("FindPendingInvitesByEmail: %v", err)
	}
	if len(invites) != 1 {
		t.Fatalf("expected 1 invite, got %d", len(invites))
	}
	if invites[0].Role != "developer" {
		t.Errorf(testRoleFmt, invites[0].Role)
	}

	listed, _ := store.ListPendingInvites(ctx, testOrgInv)
	if len(listed) != 1 {
		t.Errorf("expected 1 listed invite, got %d", len(listed))
	}

	store.DeletePendingInvite(ctx, invites[0].ID)
	remaining, _ := store.FindPendingInvitesByEmail(ctx, testNewGuyEmail)
	if len(remaining) != 0 {
		t.Errorf("expected 0 invites after delete, got %d", len(remaining))
	}
}

func TestDuplicateOrgSlug(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	store.CreateOrg(ctx, &domain.Org{ID: "org-1", Name: "First", Slug: "same-slug", Tier: domain.Free, OwnerID: userID})
	err := store.CreateOrg(ctx, &domain.Org{ID: "org-2", Name: "Second", Slug: "same-slug", Tier: domain.Free, OwnerID: userID})
	if err == nil {
		t.Error("expected error for duplicate slug")
	}
}

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
		t.Error("migration version should be > 0")
	}
}

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
