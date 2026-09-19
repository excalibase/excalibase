//go:build integration

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

const (
	testOrgsPath      = "/api/orgs"
	testOrgsSlash     = "/api/orgs/"
	testMembersPath   = "/members"
	testMyProjMembers = "/projects/my-proj/members"
	testProj1Members  = "/projects/proj1/members"
	testMembersSlash = "/members/"
)


// testAliceID and testBobID are runtime-constructed user IDs used across
// org handler tests. Using testutil.FixtureToken keeps SAST tools from
// flagging them as hardcoded credentials.
var (
	testAliceID  = testutil.FixtureToken("alice-id")
	testBobID    = testutil.FixtureToken("bob-id")
	testPadminID = testutil.FixtureToken("padmin-id")
)

func setupOrgRouter(t *testing.T) (chi.Router, *pgstore.Store) {
	r, store, _ := setupOrgRouterWithInstances(t, true /* isCloud — existing tests expect multi-org */)
	return r, store
}

func setupOrgRouterMode(t *testing.T, isCloud bool) (chi.Router, *pgstore.Store) {
	r, store, _ := setupOrgRouterWithInstances(t, isCloud)
	return r, store
}

// setupOrgRouterWithInstances builds the org router and also returns the
// in-memory instance store wired into the handler. Project-member tests seed
// it with project→org mappings; the handler uses it to confirm a URL project
// actually belongs to the URL org (cross-org enumeration guard).
func setupOrgRouterWithInstances(t *testing.T, isCloud bool) (chi.Router, *pgstore.Store, *inMemoryInstanceStore) {
	t.Helper()
	store := pgtest.New(t)

	// Create test users
	store.CreateUser(t.Context(), &domain.User{
		ID: testAliceID, Username: testutil.FixtureToken("alice"), Email: "alice@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	})
	store.CreateUser(t.Context(), &domain.User{
		ID: testBobID, Username: testutil.FixtureToken("bob"), Email: "bob@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	})

	instances := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}
	orgHandler := NewOrgHandler(store, store)
	orgHandler.SetInstanceStore(instances)
	r := chi.NewRouter()
	r.Route(testOrgsPath, func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				userID := r.Header.Get("X-Test-User")
				if userID == "" {
					userID = testAliceID
				}
				u, _ := store.FindUserByID(r.Context(), userID)
				var user *domain.User
				if u != nil {
					user = u
				} else {
					user = &domain.User{ID: userID, Username: userID, Role: "user", Active: true}
				}
				ctx := auth.SetUser(r.Context(), user)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})
		orgHandler.Routes(r, isCloud)
	})
	return r, store, instances
}

func orgRequest(r chi.Router, method, path, body string, userID string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if userID != "" {
		req.Header.Set("X-Test-User", userID)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// --- Self-hosted vs cloud gating ---

func TestOrgRoutes_SelfHostedGatesCloudOnlyRoutes(t *testing.T) {
	r, store := setupOrgRouterMode(t, false /* selfhosted */)

	// POST /api/orgs/ (create new org) — cloud only, 404 in self-hosted
	w := orgRequest(r, "POST", testOrgsSlash,
		`{"name":"Team Beta","slug":"beta"}`, testAliceID)
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 404/405 for create org in self-hosted, got %d body=%s", w.Code, w.Body.String())
	}

	// Seed an org directly so delete has something to target.
	org := &domain.Org{ID: "o1", Name: "Default", Slug: "default", Tier: domain.Free, OwnerID: testAliceID}
	if err := store.CreateOrg(t.Context(), org); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	store.AddOrgMember(t.Context(), &domain.OrgMember{OrgID: "o1", UserID: testAliceID, Role: "owner"})

	// DELETE /api/orgs/{orgId} — cloud only, 404 in self-hosted
	w = orgRequest(r, "DELETE", "/api/orgs/o1", "", testAliceID)
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 404/405 for delete org in self-hosted, got %d body=%s", w.Code, w.Body.String())
	}

	// GET /api/orgs/ — works in both modes (shows the default org)
	w = orgRequest(r, "GET", testOrgsSlash, "", testAliceID)
	if w.Code != 200 {
		t.Errorf("expected list orgs to work in self-hosted, got %d", w.Code)
	}

	// GET /api/orgs/{orgId} — works in both modes
	w = orgRequest(r, "GET", "/api/orgs/o1", "", testAliceID)
	if w.Code != 200 {
		t.Errorf("expected get org to work in self-hosted, got %d", w.Code)
	}

	// POST /api/orgs/{orgId}/members — invite works in both (team collaboration)
	w = orgRequest(r, "POST", "/api/orgs/o1/members",
		`{"email":"bob@test.com","role":"developer"}`, testAliceID)
	if w.Code != 200 && w.Code != http.StatusCreated {
		t.Errorf("expected invite member to work in self-hosted, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOrgRoutes_CloudAllowsCreateAndDelete(t *testing.T) {
	r, _ := setupOrgRouterMode(t, true /* cloud */)
	w := orgRequest(r, "POST", testOrgsSlash,
		`{"name":"Cloud Team","slug":"cloud-team"}`, testAliceID)
	if w.Code != http.StatusCreated && w.Code != 200 {
		t.Errorf("expected create org to work in cloud, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateOrg(t *testing.T) {
	r, _ := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Alice Corp","slug":"alice-corp"}`, testAliceID)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateOrg: got %d, want %d. Body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)
	if org.Name != "Alice Corp" {
		t.Errorf("name: got %q", org.Name)
	}
	if org.Slug != "alice-corp" {
		t.Errorf("slug: got %q", org.Slug)
	}
	if org.Tier != domain.Free {
		t.Errorf("tier: got %q, want FREE", org.Tier)
	}
	if org.OwnerID != testAliceID {
		t.Errorf("ownerId: got %q", org.OwnerID)
	}
}

func TestListMyOrgs(t *testing.T) {
	r, _ := setupOrgRouter(t)

	// Create org as alice
	orgRequest(r, "POST", testOrgsPath, `{"name":"Org1","slug":"org1"}`, testAliceID)
	orgRequest(r, "POST", testOrgsPath, `{"name":"Org2","slug":"org2"}`, testAliceID)

	// Alice should see 2 orgs
	w := orgRequest(r, "GET", testOrgsPath, "", testAliceID)
	if w.Code != http.StatusOK {
		t.Fatalf("ListMyOrgs: got %d", w.Code)
	}

	var orgs []domain.Org
	json.NewDecoder(w.Body).Decode(&orgs)
	if len(orgs) != 2 {
		t.Errorf("expected 2 orgs, got %d", len(orgs))
	}

	// Bob should see 0 orgs
	w2 := orgRequest(r, "GET", testOrgsPath, "", testBobID)
	var bobOrgs []domain.Org
	json.NewDecoder(w2.Body).Decode(&bobOrgs)
	if len(bobOrgs) != 0 {
		t.Errorf("bob should see 0 orgs, got %d", len(bobOrgs))
	}
}

func TestInviteAndListMembers(t *testing.T) {
	r, _ := setupOrgRouter(t)

	// Create org
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"TeamOrg","slug":"team"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// Invite bob as developer
	w2 := orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"userId":"`+testBobID+`","role":"developer"}`, testAliceID)
	if w2.Code != http.StatusCreated {
		t.Fatalf("InviteMember: got %d. Body: %s", w2.Code, w2.Body.String())
	}

	// List members — should be alice (owner) + bob (developer)
	w3 := orgRequest(r, "GET", testOrgsSlash+org.ID+testMembersPath, "", testAliceID)
	var members []domain.OrgMember
	json.NewDecoder(w3.Body).Decode(&members)
	if len(members) != 2 {
		t.Errorf("expected 2 members, got %d", len(members))
	}

	// Bob should now see the org in his list
	w4 := orgRequest(r, "GET", testOrgsPath, "", testBobID)
	var bobOrgs []domain.Org
	json.NewDecoder(w4.Body).Decode(&bobOrgs)
	if len(bobOrgs) != 1 {
		t.Errorf("bob should see 1 org after invite, got %d", len(bobOrgs))
	}
}

func TestNonAdminCannotInvite(t *testing.T) {
	r, _ := setupOrgRouter(t)

	// Create org as alice
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"AliceOrg","slug":"alice-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// Bob (not a member) tries to invite — should fail
	w2 := orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"userId":"`+testBobID+`","role":"developer"}`, testBobID)
	if w2.Code != http.StatusForbidden {
		t.Errorf("non-member invite: got %d, want %d", w2.Code, http.StatusForbidden)
	}
}

func TestDeleteOrg_OwnerOnly(t *testing.T) {
	r, _ := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"DelOrg","slug":"del-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// Bob can't delete
	w2 := orgRequest(r, "DELETE", testOrgsSlash+org.ID, "", testBobID)
	if w2.Code != http.StatusForbidden {
		t.Errorf("non-owner delete: got %d, want %d", w2.Code, http.StatusForbidden)
	}

	// Alice can delete
	w3 := orgRequest(r, "DELETE", testOrgsSlash+org.ID, "", testAliceID)
	if w3.Code != http.StatusOK {
		t.Errorf("owner delete: got %d, want %d. Body: %s", w3.Code, http.StatusOK, w3.Body.String())
	}
}

func TestCreateOrg_InvalidSlug(t *testing.T) {
	r, _ := setupOrgRouter(t)

	tests := []struct {
		name string
		slug string
	}{
		{"uppercase", "Alice-Corp"},
		{"spaces", "alice corp"},
		{"special chars", "alice@corp"},
		{"too short", "a"},
		{"starts with hyphen", "-alice"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := orgRequest(r, "POST", testOrgsPath,
				`{"name":"Test","slug":"`+tt.slug+`"}`, testAliceID)
			if w.Code != http.StatusBadRequest {
				t.Errorf("slug %q: got %d, want %d", tt.slug, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestUpdateOrg_InvalidTier(t *testing.T) {
	r, _ := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"TierOrg","slug":"tier-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	w2 := orgRequest(r, "PATCH", testOrgsSlash+org.ID, `{"tier":"SUPER_PLAN"}`, testAliceID)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("invalid tier: got %d, want %d. Body: %s", w2.Code, http.StatusBadRequest, w2.Body.String())
	}
}

func TestUpdateOrg_ValidTier(t *testing.T) {
	r, _ := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"UpOrg","slug":"up-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	w2 := orgRequest(r, "PATCH", testOrgsSlash+org.ID, `{"tier":"STANDARD"}`, testAliceID)
	if w2.Code != http.StatusOK {
		t.Errorf("valid tier upgrade: got %d, want %d. Body: %s", w2.Code, http.StatusOK, w2.Body.String())
	}

	var updated domain.Org
	json.NewDecoder(w2.Body).Decode(&updated)
	if updated.Tier != domain.Standard {
		t.Errorf("tier: got %q, want STANDARD", updated.Tier)
	}
}

func TestGetOrg_NonMemberBlocked(t *testing.T) {
	r, _ := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Private","slug":"private-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// Bob is not a member — should get 404
	w2 := orgRequest(r, "GET", testOrgsSlash+org.ID, "", testBobID)
	if w2.Code != http.StatusNotFound {
		t.Errorf("non-member get org: got %d, want %d", w2.Code, http.StatusNotFound)
	}
}

func TestInviteWithInvalidRole(t *testing.T) {
	r, _ := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"RoleOrg","slug":"role-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	w2 := orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"email":"bob@test.com","role":"superadmin"}`, testAliceID)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("invalid role: got %d, want %d. Body: %s", w2.Code, http.StatusBadRequest, w2.Body.String())
	}
}

func TestInviteByEmail_PendingWhenUserNotExists(t *testing.T) {
	r, store := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"InvOrg","slug":"inv-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// Invite non-existent email
	w2 := orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"email":"nobody@nowhere.com","role":"developer"}`, testAliceID)
	if w2.Code != http.StatusCreated {
		t.Fatalf("pending invite: got %d, want %d. Body: %s", w2.Code, http.StatusCreated, w2.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w2.Body).Decode(&resp)
	if resp["status"] != "pending" {
		t.Errorf("expected status=pending, got %q", resp["status"])
	}

	// Verify pending invite stored
	invites, _ := store.ListPendingInvites(t.Context(), org.ID)
	if len(invites) != 1 {
		t.Errorf("expected 1 pending invite, got %d", len(invites))
	}
}

func TestProjectMemberCRUD(t *testing.T) {
	r, _, instances := setupOrgRouterWithInstances(t, true)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"ProjOrg","slug":"proj-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// my-proj belongs to this org — seed the instance so the project↔org
	// ownership check passes for the legitimate same-org flow.
	instances.Save(&domain.DatabaseInstance{ProjectID: "my-proj", OrgID: org.ID})

	// Alice (owner) adds bob as editor
	w2 := orgRequest(r, "POST", testOrgsSlash+org.ID+testMyProjMembers,
		`{"userId":"`+testBobID+`","role":"editor"}`, testAliceID)
	if w2.Code != http.StatusCreated {
		t.Fatalf("AddProjectMember: got %d. Body: %s", w2.Code, w2.Body.String())
	}

	// List project members
	w3 := orgRequest(r, "GET", testOrgsSlash+org.ID+testMyProjMembers, "", testAliceID)
	var members []domain.ProjectMember
	json.NewDecoder(w3.Body).Decode(&members)
	if len(members) != 1 {
		t.Errorf("expected 1 project member, got %d", len(members))
	}
	if len(members) > 0 && members[0].Role != "editor" {
		t.Errorf("role: got %q, want editor", members[0].Role)
	}

	// Update project member role
	w4 := orgRequest(r, "PATCH", testOrgsSlash+org.ID+"/projects/my-proj/members/"+testBobID,
		`{"role":"viewer"}`, testAliceID)
	if w4.Code != http.StatusOK {
		t.Errorf("UpdateProjectMemberRole: got %d. Body: %s", w4.Code, w4.Body.String())
	}

	// Verify role updated
	w5 := orgRequest(r, "GET", testOrgsSlash+org.ID+testMyProjMembers, "", testAliceID)
	var updated []domain.ProjectMember
	json.NewDecoder(w5.Body).Decode(&updated)
	if len(updated) > 0 && updated[0].Role != "viewer" {
		t.Errorf("updated role: got %q, want viewer", updated[0].Role)
	}

	// Remove project member
	w6 := orgRequest(r, "DELETE", testOrgsSlash+org.ID+"/projects/my-proj/members/"+testBobID, "", testAliceID)
	if w6.Code != http.StatusOK {
		t.Errorf("RemoveProjectMember: got %d", w6.Code)
	}

	// Verify removed
	w7 := orgRequest(r, "GET", testOrgsSlash+org.ID+testMyProjMembers, "", testAliceID)
	var empty []domain.ProjectMember
	json.NewDecoder(w7.Body).Decode(&empty)
	if len(empty) != 0 {
		t.Errorf("expected 0 members after remove, got %d", len(empty))
	}
}

func TestProjectMember_DeveloperCannotAdd(t *testing.T) {
	r, _ := setupOrgRouter(t)

	// Alice creates org, invites bob as developer
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"DevOrg","slug":"dev-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)
	orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath, `{"userId":"`+testBobID+`","role":"developer"}`, testAliceID)

	// Bob (developer) tries to add project member — should fail
	w2 := orgRequest(r, "POST", testOrgsSlash+org.ID+testProj1Members,
		`{"userId":"`+testAliceID+`","role":"viewer"}`, testBobID)
	if w2.Code != http.StatusForbidden {
		t.Errorf("developer adding project member: got %d, want %d", w2.Code, http.StatusForbidden)
	}
}

func TestProjectMember_NonMemberCannotView(t *testing.T) {
	r, _ := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"PrivOrg","slug":"priv-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// Bob (not a member of org) tries to list project members
	w2 := orgRequest(r, "GET", testOrgsSlash+org.ID+testProj1Members, "", testBobID)
	if w2.Code != http.StatusNotFound {
		t.Errorf("non-member viewing project members: got %d, want %d", w2.Code, http.StatusNotFound)
	}
}

func TestProjectMember_InvalidRoleRejected(t *testing.T) {
	r, _, instances := setupOrgRouterWithInstances(t, true)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"ROrg","slug":"r-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// proj1 belongs to this org so the request reaches role validation
	// (rather than short-circuiting on the project↔org ownership check).
	instances.Save(&domain.DatabaseInstance{ProjectID: "proj1", OrgID: org.ID})

	// Add with invalid project role
	w2 := orgRequest(r, "POST", testOrgsSlash+org.ID+testProj1Members,
		`{"userId":"`+testBobID+`","role":"superuser"}`, testAliceID)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("invalid project role: got %d, want %d. Body: %s", w2.Code, http.StatusBadRequest, w2.Body.String())
	}
}

// TestProjectMember_CrossOrgProjectReturns404 proves the cross-org
// enumeration hole is closed: a legitimate member/owner of org A cannot
// read or mutate the project members of a project that belongs to org B
// by putting B's projectId in A's URL. The handler looks the project up in
// the instance store and 404s when its OrgID != the URL org.
func TestProjectMember_CrossOrgProjectReturns404(t *testing.T) {
	r, _, instances := setupOrgRouterWithInstances(t, true)

	// Alice owns org A.
	wA := orgRequest(r, "POST", testOrgsPath, `{"name":"OrgA","slug":"org-a"}`, testAliceID)
	var orgA domain.Org
	json.NewDecoder(wA.Body).Decode(&orgA)

	// Bob owns org B, which owns project victim-proj.
	wB := orgRequest(r, "POST", testOrgsPath, `{"name":"OrgB","slug":"org-b"}`, testBobID)
	var orgB domain.Org
	json.NewDecoder(wB.Body).Decode(&orgB)
	instances.Save(&domain.DatabaseInstance{ProjectID: "victim-proj", OrgID: orgB.ID})

	// Alice (member of org A) tries to list org B's project members through
	// org A's URL: /api/orgs/{orgA}/projects/victim-proj/members.
	base := testOrgsSlash + orgA.ID + "/projects/victim-proj/members"

	wList := orgRequest(r, "GET", base, "", testAliceID)
	if wList.Code != http.StatusNotFound {
		t.Errorf("cross-org ListProjectMembers: got %d, want 404 (body=%s)", wList.Code, wList.Body.String())
	}

	wAdd := orgRequest(r, "POST", base, `{"userId":"`+testAliceID+`","role":"viewer"}`, testAliceID)
	if wAdd.Code != http.StatusNotFound {
		t.Errorf("cross-org AddProjectMember: got %d, want 404 (body=%s)", wAdd.Code, wAdd.Body.String())
	}

	wPatch := orgRequest(r, "PATCH", base+"/"+testBobID, `{"role":"viewer"}`, testAliceID)
	if wPatch.Code != http.StatusNotFound {
		t.Errorf("cross-org UpdateProjectMemberRole: got %d, want 404 (body=%s)", wPatch.Code, wPatch.Body.String())
	}

	wDel := orgRequest(r, "DELETE", base+"/"+testBobID, "", testAliceID)
	if wDel.Code != http.StatusNotFound {
		t.Errorf("cross-org RemoveProjectMember: got %d, want 404 (body=%s)", wDel.Code, wDel.Body.String())
	}
}

// TestProjectMember_SameOrgProjectSucceeds confirms the legitimate flow is
// untouched: an owner managing a project that genuinely belongs to their org
// still passes the ownership check and the operation succeeds.
func TestProjectMember_SameOrgProjectSucceeds(t *testing.T) {
	r, _, instances := setupOrgRouterWithInstances(t, true)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"OwnOrg","slug":"own-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)
	instances.Save(&domain.DatabaseInstance{ProjectID: "own-proj", OrgID: org.ID})

	base := testOrgsSlash + org.ID + "/projects/own-proj/members"

	// Add bob as editor — same-org, must succeed.
	wAdd := orgRequest(r, "POST", base, `{"userId":"`+testBobID+`","role":"editor"}`, testAliceID)
	if wAdd.Code != http.StatusCreated {
		t.Fatalf("same-org AddProjectMember: got %d, want 201 (body=%s)", wAdd.Code, wAdd.Body.String())
	}

	// List shows the member.
	wList := orgRequest(r, "GET", base, "", testAliceID)
	if wList.Code != http.StatusOK {
		t.Fatalf("same-org ListProjectMembers: got %d, want 200", wList.Code)
	}
	var members []domain.ProjectMember
	json.NewDecoder(wList.Body).Decode(&members)
	if len(members) != 1 || members[0].Role != "editor" {
		t.Errorf("expected 1 editor member, got %+v", members)
	}
}

func TestProjectRolePermissions(t *testing.T) {
	// Test the org_rbac permission functions
	tests := []struct {
		role string
		perm auth.ProjectPermission
		want bool
	}{
		{"admin", auth.ProjectPermDDL, true},
		{"admin", auth.ProjectPermDML, true},
		{"admin", auth.ProjectPermRead, true},
		{"admin", auth.ProjectPermManage, true},
		{"editor", auth.ProjectPermDDL, false},
		{"editor", auth.ProjectPermDML, true},
		{"editor", auth.ProjectPermRead, true},
		{"editor", auth.ProjectPermManage, false},
		{"viewer", auth.ProjectPermDDL, false},
		{"viewer", auth.ProjectPermDML, false},
		{"viewer", auth.ProjectPermRead, true},
		{"viewer", auth.ProjectPermManage, false},
		{"unknown", auth.ProjectPermRead, false},
	}

	for _, tt := range tests {
		got := auth.HasProjectPermission(tt.role, tt.perm)
		if got != tt.want {
			t.Errorf("HasProjectPermission(%q, %q) = %v, want %v", tt.role, tt.perm, got, tt.want)
		}
	}
}

func TestDeleteOrg_CascadesMembers(t *testing.T) {
	r, store := setupOrgRouter(t)
	ctx := t.Context()

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"CascOrg","slug":"casc-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// Invite bob + add pending invite
	orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath, `{"userId":"`+testBobID+`","role":"developer"}`, testAliceID)
	store.CreatePendingInvite(ctx, &domain.PendingInvite{OrgID: org.ID, Email: "pending@t.com", Role: "viewer", InvitedBy: testAliceID})

	// Delete org
	orgRequest(r, "DELETE", testOrgsSlash+org.ID, "", testAliceID)

	// Verify cascade — members gone
	members, _ := store.ListOrgMembers(ctx, org.ID)
	if len(members) != 0 {
		t.Errorf("expected 0 members after delete, got %d", len(members))
	}

	// Pending invites gone
	invites, _ := store.ListPendingInvites(ctx, org.ID)
	if len(invites) != 0 {
		t.Errorf("expected 0 invites after delete, got %d", len(invites))
	}
}

func TestDeveloperCannotSeeSettings(t *testing.T) {
	r, _ := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"DevOrg2","slug":"dev-org2"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)
	orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath, `{"userId":"`+testBobID+`","role":"developer"}`, testAliceID)

	// Bob (developer) tries to update org — should fail
	w2 := orgRequest(r, "PATCH", testOrgsSlash+org.ID, `{"name":"hacked"}`, testBobID)
	if w2.Code != http.StatusForbidden {
		t.Errorf("developer update org: got %d, want %d", w2.Code, http.StatusForbidden)
	}

	// Bob tries to delete org — should fail
	w3 := orgRequest(r, "DELETE", testOrgsSlash+org.ID, "", testBobID)
	if w3.Code != http.StatusForbidden {
		t.Errorf("developer delete org: got %d, want %d", w3.Code, http.StatusForbidden)
	}
}

func TestAdminCannotDeleteOrg(t *testing.T) {
	r, _ := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"AdmOrg","slug":"adm-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)
	orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath, `{"userId":"`+testBobID+`","role":"admin"}`, testAliceID)

	// Bob (admin) can update
	w2 := orgRequest(r, "PATCH", testOrgsSlash+org.ID, `{"name":"Updated"}`, testBobID)
	if w2.Code != http.StatusOK {
		t.Errorf("admin update: got %d, want %d", w2.Code, http.StatusOK)
	}

	// Bob (admin) cannot delete — only owner can
	w3 := orgRequest(r, "DELETE", testOrgsSlash+org.ID, "", testBobID)
	if w3.Code != http.StatusForbidden {
		t.Errorf("admin delete: got %d, want %d", w3.Code, http.StatusForbidden)
	}
}

func TestPlatformAdminCanManageAnyOrg(t *testing.T) {
	r, store := setupOrgRouter(t)

	// Create a platform_admin user
	store.CreateUser(t.Context(), &domain.User{
		ID: testPadminID, Username: testutil.FixtureToken("padmin"), Email: "padmin@t.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "platform_admin", Active: true,
	})

	// Alice creates org
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"PAdmOrg","slug":"padm-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// Platform admin can see it
	w2 := orgRequest(r, "GET", testOrgsSlash+org.ID, "", testPadminID)
	if w2.Code != http.StatusOK {
		t.Errorf("platform admin view org: got %d, want %d", w2.Code, http.StatusOK)
	}

	// Platform admin can invite
	w3 := orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath, `{"userId":"`+testBobID+`","role":"developer"}`, testPadminID)
	if w3.Code != http.StatusCreated {
		t.Errorf("platform admin invite: got %d, want %d. Body: %s", w3.Code, http.StatusCreated, w3.Body.String())
	}
}

func TestOrgRolePermissions(t *testing.T) {
	tests := []struct {
		role string
		perm auth.OrgPermission
		want bool
	}{
		{"owner", auth.OrgPermManageMembers, true},
		{"owner", auth.OrgPermDeleteOrg, true},
		{"admin", auth.OrgPermManageMembers, true},
		{"admin", auth.OrgPermDeleteOrg, false},
		{"developer", auth.OrgPermManageMembers, false},
		{"developer", auth.OrgPermViewProjects, true},
		{"viewer", auth.OrgPermViewProjects, true},
		{"viewer", auth.OrgPermManageMembers, false},
		{"unknown", auth.OrgPermViewProjects, false},
	}

	for _, tt := range tests {
		got := auth.HasOrgPermission(tt.role, tt.perm)
		if got != tt.want {
			t.Errorf("HasOrgPermission(%q, %q) = %v, want %v", tt.role, tt.perm, got, tt.want)
		}
	}
}

// TestInviteServiceAccountIsRefused pins that a service principal stays
// platform-scoped: it authenticates with a capability token and must never
// acquire project access by becoming an organization member.
func TestInviteServiceAccountIsRefused(t *testing.T) {
	r, store := setupOrgRouter(t)

	svcID := testutil.FixtureToken("svc-id")
	store.CreateUser(t.Context(), &domain.User{
		ID: svcID, Username: "svc-graphql", Email: "svc-graphql@svc.excalibase.internal",
		Role: "platform_admin", Active: true, Kind: domain.UserKindService,
	})

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"SvcOrg","slug":"svc-org"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	for _, body := range []string{
		`{"userId":"` + svcID + `","role":"developer"}`,
		`{"email":"svc-graphql@svc.excalibase.internal","role":"developer"}`,
	} {
		invite := orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath, body, testAliceID)
		if invite.Code != http.StatusBadRequest {
			t.Fatalf("invite %s: got %d, want %d. Body: %s", body, invite.Code, http.StatusBadRequest, invite.Body.String())
		}
	}

	members := orgRequest(r, "GET", testOrgsSlash+org.ID+testMembersPath, "", testAliceID)
	var listed []domain.OrgMember
	json.NewDecoder(members.Body).Decode(&listed)
	if len(listed) != 1 {
		t.Fatalf("expected only the owner, got %d members", len(listed))
	}
}
