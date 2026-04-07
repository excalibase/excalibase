package sqlite

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func setupOrgTest(t *testing.T) (*Store, string) {
	t.Helper()
	store := testStore(t)
	ctx := context.Background()

	// Create a platform user for org ownership
	store.CreateUser(ctx, &domain.User{
		ID: "user-1", Username: "alice", Email: "alice@test.com",
		PasswordHash: "hash", Role: "user", Active: true,
	})
	return store, "user-1"
}

func TestCreateAndFindOrg(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	org := &domain.Org{
		ID: "org-1", Name: "Alice Corp", Slug: "alice-corp",
		Tier: domain.Free, OwnerID: userID,
	}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	// Find by ID
	got, err := store.FindOrgByID(ctx, "org-1")
	if err != nil {
		t.Fatalf("FindOrgByID: %v", err)
	}
	if got.Name != "Alice Corp" {
		t.Errorf("name: got %q, want %q", got.Name, "Alice Corp")
	}
	if got.Slug != "alice-corp" {
		t.Errorf("slug: got %q, want %q", got.Slug, "alice-corp")
	}
	if got.Tier != domain.Free {
		t.Errorf("tier: got %q, want %q", got.Tier, domain.Free)
	}

	// Find by slug
	got2, err := store.FindOrgBySlug(ctx, "alice-corp")
	if err != nil {
		t.Fatalf("FindOrgBySlug: %v", err)
	}
	if got2.ID != "org-1" {
		t.Errorf("id: got %q, want %q", got2.ID, "org-1")
	}
}

func TestFindOrgsByUser(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	// Create 2 orgs
	store.CreateOrg(ctx, &domain.Org{ID: "org-a", Name: "Org A", Slug: "org-a", Tier: domain.Free, OwnerID: userID})
	store.CreateOrg(ctx, &domain.Org{ID: "org-b", Name: "Org B", Slug: "org-b", Tier: domain.Free, OwnerID: userID})

	// Add user as member of both
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

func TestUpdateOrg(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	store.CreateOrg(ctx, &domain.Org{ID: "org-u", Name: "Old", Slug: "old", Tier: domain.Free, OwnerID: userID})

	org, _ := store.FindOrgByID(ctx, "org-u")
	org.Name = "New Name"
	org.Tier = domain.Standard
	if err := store.UpdateOrg(ctx, org); err != nil {
		t.Fatalf("UpdateOrg: %v", err)
	}

	got, _ := store.FindOrgByID(ctx, "org-u")
	if got.Name != "New Name" {
		t.Errorf("name: got %q", got.Name)
	}
	if got.Tier != domain.Standard {
		t.Errorf("tier: got %q", got.Tier)
	}
}

func TestDeleteOrg(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	store.CreateOrg(ctx, &domain.Org{ID: "org-d", Name: "Del", Slug: "del", Tier: domain.Free, OwnerID: userID})
	if err := store.DeleteOrg(ctx, "org-d"); err != nil {
		t.Fatalf("DeleteOrg: %v", err)
	}

	got, err := store.FindOrgByID(ctx, "org-d")
	if err == nil && got != nil {
		t.Error("expected org to be deleted")
	}
}

func TestOrgMemberCRUD(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	// Create second user
	store.CreateUser(ctx, &domain.User{
		ID: "user-2", Username: "bob", Email: "bob@test.com",
		PasswordHash: "hash", Role: "user", Active: true,
	})

	store.CreateOrg(ctx, &domain.Org{ID: "org-m", Name: "Members", Slug: "members", Tier: domain.Free, OwnerID: userID})

	// Add owner
	store.AddOrgMember(ctx, &domain.OrgMember{OrgID: "org-m", UserID: userID, Role: domain.OrgRoleOwner})

	// Add member
	if err := store.AddOrgMember(ctx, &domain.OrgMember{OrgID: "org-m", UserID: "user-2", Role: domain.OrgRoleDeveloper}); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	// List
	members, err := store.ListOrgMembers(ctx, "org-m")
	if err != nil {
		t.Fatalf("ListOrgMembers: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("expected 2 members, got %d", len(members))
	}

	// Get specific member — should include email/username from join
	m, err := store.GetOrgMember(ctx, "org-m", "user-2")
	if err != nil {
		t.Fatalf("GetOrgMember: %v", err)
	}
	if m.Role != domain.OrgRoleDeveloper {
		t.Errorf("role: got %q", m.Role)
	}
	if m.Email != "bob@test.com" {
		t.Errorf("email: got %q", m.Email)
	}

	// Update role
	if err := store.UpdateOrgMemberRole(ctx, "org-m", "user-2", domain.OrgRoleAdmin); err != nil {
		t.Fatalf("UpdateOrgMemberRole: %v", err)
	}
	m2, _ := store.GetOrgMember(ctx, "org-m", "user-2")
	if m2.Role != domain.OrgRoleAdmin {
		t.Errorf("updated role: got %q", m2.Role)
	}

	// Remove
	if err := store.RemoveOrgMember(ctx, "org-m", "user-2"); err != nil {
		t.Fatalf("RemoveOrgMember: %v", err)
	}
	members2, _ := store.ListOrgMembers(ctx, "org-m")
	if len(members2) != 1 {
		t.Errorf("expected 1 member after remove, got %d", len(members2))
	}
}

func TestProjectMemberCRUD(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	store.CreateUser(ctx, &domain.User{
		ID: "user-3", Username: "charlie", Email: "charlie@test.com",
		PasswordHash: "hash", Role: "user", Active: true,
	})

	store.CreateOrg(ctx, &domain.Org{ID: "org-p", Name: "ProjOrg", Slug: "proj-org", Tier: domain.Free, OwnerID: userID})

	// Add project member
	if err := store.AddProjectMember(ctx, &domain.ProjectMember{
		ProjectID: "my-project", OrgID: "org-p", UserID: "user-3", Role: domain.ProjectRoleEditor,
	}); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	// List
	members, err := store.ListProjectMembers(ctx, "my-project")
	if err != nil {
		t.Fatalf("ListProjectMembers: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("expected 1 member, got %d", len(members))
	}

	// Get — verify join
	m, _ := store.GetProjectMember(ctx, "my-project", "user-3")
	if m.Role != domain.ProjectRoleEditor {
		t.Errorf("role: got %q", m.Role)
	}
	if m.Username != "charlie" {
		t.Errorf("username: got %q", m.Username)
	}

	// Update role
	store.UpdateProjectMemberRole(ctx, "my-project", "user-3", domain.ProjectRoleViewer)
	m2, _ := store.GetProjectMember(ctx, "my-project", "user-3")
	if m2.Role != domain.ProjectRoleViewer {
		t.Errorf("updated role: got %q", m2.Role)
	}

	// Remove
	store.RemoveProjectMember(ctx, "my-project", "user-3")
	members2, _ := store.ListProjectMembers(ctx, "my-project")
	if len(members2) != 0 {
		t.Errorf("expected 0 members after remove, got %d", len(members2))
	}
}

func TestFindOrgNotFound(t *testing.T) {
	store, _ := setupOrgTest(t)
	ctx := context.Background()

	_, err := store.FindOrgByID(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent org")
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
