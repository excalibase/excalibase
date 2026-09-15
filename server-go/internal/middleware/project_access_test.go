package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const (
	testProject      = "proj-1"
	testOtherProject = "proj-2"
	testOrg          = "org-1"
	testUserRole     = "user"
	testPlatformRole = "platform_admin"
)

// countingOrgStore records how many membership lookups the chain performs so
// the tests can pin that RequireProjectRole reuses the resolution made by
// RequireProjectAccess instead of hitting the store a second time.
type countingOrgStore struct {
	*fakestore.Orgs
	lookups int
}

func (s *countingOrgStore) GetOrgMember(ctx context.Context, orgID, userID string) (*domain.OrgMember, error) {
	s.lookups++
	return s.Orgs.GetOrgMember(ctx, orgID, userID)
}

// projectRequest builds a request routed to {projectId} with an optional
// authenticated user and token attached.
func projectRequest(method, projectID string, user *domain.User, token *domain.AccessToken) *http.Request {
	r := httptest.NewRequest(method, "/api/provision/"+projectID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("projectId", projectID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	if user != nil {
		r = r.WithContext(auth.SetUser(r.Context(), user))
	}
	if token != nil {
		r = r.WithContext(auth.SetToken(r.Context(), token))
	}
	return r
}

func memberUser(id string) *domain.User { return &domain.User{ID: id, Role: testUserRole} }

// storesWithMember seeds one project in testOrg and one member with the role.
func storesWithMember(userID, role string) (*fakestore.Instances, *fakestore.Orgs) {
	inst := fakestore.NewInstances()
	inst.Save(&domain.DatabaseInstance{ProjectID: testProject, OrgID: testOrg})
	orgs := fakestore.NewOrgs()
	orgs.AddMember(testOrg, userID, role)
	return inst, orgs
}

func serve(t *testing.T, mw func(http.Handler) http.Handler, r *http.Request) int {
	t.Helper()
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusOK && !called {
		t.Error("200 returned but next handler not called")
	}
	return w.Code
}

func TestRequireProjectAccess_NoProjectIDPassesThrough(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if code := serve(t, RequireProjectAccess(fakestore.NewInstances(), fakestore.NewOrgs()), r); code != http.StatusOK {
		t.Errorf("no projectId should pass through, got %d", code)
	}
}

// TestRequireProjectAccess_Matrix pins the visibility rules: anything the
// caller may not see is a 404 (never a 403, which would confirm the project
// exists), a bound token never leaves its project, and a read-only token
// cannot mutate.
func TestRequireProjectAccess_Matrix(t *testing.T) {
	memberID := testutil.FixturePassword("member")
	inst, orgs := storesWithMember(memberID, domain.OrgRoleViewer)
	cases := []struct {
		name    string
		method  string
		project string
		user    *domain.User
		token   *domain.AccessToken
		want    int
	}{
		{"anonymous", http.MethodGet, testProject, nil, nil, http.StatusUnauthorized},
		{"member sees project", http.MethodGet, testProject, memberUser(memberID), nil, http.StatusOK},
		{"unknown project", http.MethodGet, "ghost", memberUser(memberID), nil, http.StatusNotFound},
		{"non-member gets 404 not 403", http.MethodGet, testProject, memberUser("stranger"), nil, http.StatusNotFound},
		{"token bound to this project", http.MethodGet, testProject, memberUser(memberID), &domain.AccessToken{ProjectID: testProject}, http.StatusOK},
		{"token bound to another project", http.MethodGet, testProject, memberUser(memberID), &domain.AccessToken{ProjectID: testOtherProject}, http.StatusNotFound},
		{"read scope may GET", http.MethodGet, testProject, memberUser(memberID), &domain.AccessToken{Scopes: auth.ScopeRead}, http.StatusOK},
		{"read scope may not POST", http.MethodPost, testProject, memberUser(memberID), &domain.AccessToken{Scopes: auth.ScopeRead}, http.StatusForbidden},
		{"write scope may POST", http.MethodPost, testProject, memberUser(memberID), &domain.AccessToken{Scopes: auth.ScopeWrite}, http.StatusOK},
		{"platform admin bypasses membership", http.MethodGet, "ghost", &domain.User{ID: "op", Role: testPlatformRole}, nil, http.StatusOK},
		{"platform admin still bound by token project", http.MethodGet, testProject, &domain.User{ID: "op", Role: testPlatformRole}, &domain.AccessToken{ProjectID: testOtherProject}, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := projectRequest(tc.method, tc.project, tc.user, tc.token)
			if code := serve(t, RequireProjectAccess(inst, orgs), r); code != tc.want {
				t.Errorf("got %d want %d", code, tc.want)
			}
		})
	}
}

func TestRequireProjectAccess_FailsClosed(t *testing.T) {
	memberID := testutil.FixturePassword("member")
	t.Run("instance without org", func(t *testing.T) {
		inst := fakestore.NewInstances()
		inst.Save(&domain.DatabaseInstance{ProjectID: testProject})
		orgs := fakestore.NewOrgs()
		orgs.AddMember(testOrg, memberID, domain.OrgRoleOwner)
		r := projectRequest(http.MethodGet, testProject, memberUser(memberID), nil)
		if code := serve(t, RequireProjectAccess(inst, orgs), r); code != http.StatusNotFound {
			t.Errorf("got %d want 404", code)
		}
	})
	t.Run("nil org store", func(t *testing.T) {
		inst, _ := storesWithMember(memberID, domain.OrgRoleOwner)
		r := projectRequest(http.MethodGet, testProject, memberUser(memberID), nil)
		if code := serve(t, RequireProjectAccess(inst, nil), r); code != http.StatusNotFound {
			t.Errorf("got %d want 404", code)
		}
	})
	t.Run("membership lookup error", func(t *testing.T) {
		inst, orgs := storesWithMember(memberID, domain.OrgRoleOwner)
		orgs.Err = context.DeadlineExceeded
		r := projectRequest(http.MethodGet, testProject, memberUser(memberID), nil)
		if code := serve(t, RequireProjectAccess(inst, orgs), r); code != http.StatusNotFound {
			t.Errorf("got %d want 404", code)
		}
	})
}

// TestRequireProjectRole_Hierarchy pins the tenant-plane RBAC: Owner ⊇ Admin ⊇
// Developer ⊇ Viewer. A viewer can never reach a developer/admin route; a
// developer can never reach an admin route; higher roles inherit lower ones.
func TestRequireProjectRole_Hierarchy(t *testing.T) {
	cases := []struct {
		name, memberRole, minRole string
		want                      int
	}{
		{"viewer blocked from admin", domain.OrgRoleViewer, domain.OrgRoleAdmin, http.StatusForbidden},
		{"viewer blocked from developer", domain.OrgRoleViewer, domain.OrgRoleDeveloper, http.StatusForbidden},
		{"developer blocked from admin", domain.OrgRoleDeveloper, domain.OrgRoleAdmin, http.StatusForbidden},
		{"developer allowed developer", domain.OrgRoleDeveloper, domain.OrgRoleDeveloper, http.StatusOK},
		{"admin allowed developer (cumulative)", domain.OrgRoleAdmin, domain.OrgRoleDeveloper, http.StatusOK},
		{"admin allowed admin", domain.OrgRoleAdmin, domain.OrgRoleAdmin, http.StatusOK},
		{"owner allowed admin (cumulative)", domain.OrgRoleOwner, domain.OrgRoleAdmin, http.StatusOK},
		{"unknown role denied", "guest", domain.OrgRoleViewer, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid := "u-" + tc.memberRole
			inst, orgs := storesWithMember(uid, tc.memberRole)
			r := projectRequest(http.MethodGet, testProject, memberUser(uid), nil)
			if code := serve(t, RequireProjectRole(tc.minRole, inst, orgs), r); code != tc.want {
				t.Errorf("got %d want %d", code, tc.want)
			}
		})
	}
}

func TestRequireProjectRole_Visibility(t *testing.T) {
	memberID := testutil.FixturePassword("member")
	inst, orgs := storesWithMember(memberID, domain.OrgRoleOwner)
	cases := []struct {
		name string
		user *domain.User
		want int
	}{
		{"unauthenticated", nil, http.StatusUnauthorized},
		{"non-member gets 404", memberUser("stranger"), http.StatusNotFound},
		{"platform admin bypasses", &domain.User{ID: "op", Role: testPlatformRole}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := projectRequest(http.MethodGet, testProject, tc.user, nil)
			if code := serve(t, RequireProjectRole(domain.OrgRoleAdmin, inst, orgs), r); code != tc.want {
				t.Errorf("got %d want %d", code, tc.want)
			}
		})
	}
}

// TestRequireProjectRole_ReusesAccessResolution: when chained after
// RequireProjectAccess the role gate must not re-query membership.
func TestRequireProjectRole_ReusesAccessResolution(t *testing.T) {
	memberID := testutil.FixturePassword("member")
	inst, orgs := storesWithMember(memberID, domain.OrgRoleDeveloper)
	counting := &countingOrgStore{Orgs: orgs}
	chain := func(next http.Handler) http.Handler {
		return RequireProjectAccess(inst, counting)(RequireProjectRole(domain.OrgRoleDeveloper, inst, counting)(next))
	}
	r := projectRequest(http.MethodPost, testProject, memberUser(memberID), nil)
	if code := serve(t, chain, r); code != http.StatusOK {
		t.Fatalf("developer should pass, got %d", code)
	}
	if counting.lookups != 1 {
		t.Errorf("membership looked up %d times, want 1", counting.lookups)
	}
}

func TestRequireProjectRoleForWrites(t *testing.T) {
	cases := []struct {
		name, memberRole, method string
		want                     int
	}{
		{"viewer may read", domain.OrgRoleViewer, http.MethodGet, http.StatusOK},
		{"viewer may not write", domain.OrgRoleViewer, http.MethodPost, http.StatusForbidden},
		{"developer may write", domain.OrgRoleDeveloper, http.MethodDelete, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid := "u-" + tc.memberRole
			inst, orgs := storesWithMember(uid, tc.memberRole)
			r := projectRequest(tc.method, testProject, memberUser(uid), nil)
			if code := serve(t, RequireProjectRoleForWrites(domain.OrgRoleDeveloper, inst, orgs), r); code != tc.want {
				t.Errorf("got %d want %d", code, tc.want)
			}
		})
	}
}
