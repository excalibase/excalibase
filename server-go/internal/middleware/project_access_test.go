package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

// fakeInstanceStore implements storage.InstanceStore with a single
// project lookup; everything else is a no-op for these tests.
type fakeInstanceStore struct {
	inst *domain.DatabaseInstance
	err  error
}

func (f *fakeInstanceStore) Save(*domain.DatabaseInstance) error { return nil }
func (f *fakeInstanceStore) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return f.inst, f.err
}
func (f *fakeInstanceStore) FindByOwner(string) ([]*domain.DatabaseInstance, error) { return nil, nil }
func (f *fakeInstanceStore) FindAll() ([]*domain.DatabaseInstance, error)           { return nil, nil }
func (f *fakeInstanceStore) Delete(string) error                                    { return nil }

// fakeOrgStore implements storage.OrgStore; only GetOrgMember is meaningful.
type fakeOrgStore struct {
	member *domain.OrgMember
	err    error
}

func (f *fakeOrgStore) CreateOrg(context.Context, *domain.Org) error             { return nil }
func (f *fakeOrgStore) FindOrgByID(context.Context, string) (*domain.Org, error) { return nil, nil }
func (f *fakeOrgStore) FindOrgBySlug(context.Context, string) (*domain.Org, error) {
	return nil, nil
}
func (f *fakeOrgStore) FindOrgsByUser(context.Context, string) ([]*domain.Org, error) {
	return nil, nil
}
func (f *fakeOrgStore) FindAllOrgs(context.Context) ([]*domain.Org, error) { return nil, nil }
func (f *fakeOrgStore) UpdateOrg(context.Context, *domain.Org) error       { return nil }
func (f *fakeOrgStore) DeleteOrg(context.Context, string) error            { return nil }
func (f *fakeOrgStore) AddOrgMember(context.Context, *domain.OrgMember) error {
	return nil
}
func (f *fakeOrgStore) RemoveOrgMember(context.Context, string, string) error { return nil }
func (f *fakeOrgStore) UpdateOrgMemberRole(context.Context, string, string, string) error {
	return nil
}
func (f *fakeOrgStore) ListOrgMembers(context.Context, string) ([]*domain.OrgMember, error) {
	return nil, nil
}
func (f *fakeOrgStore) GetOrgMember(context.Context, string, string) (*domain.OrgMember, error) {
	return f.member, f.err
}
func (f *fakeOrgStore) AddProjectMember(context.Context, *domain.ProjectMember) error { return nil }
func (f *fakeOrgStore) RemoveProjectMember(context.Context, string, string) error     { return nil }
func (f *fakeOrgStore) UpdateProjectMemberRole(context.Context, string, string, string) error {
	return nil
}
func (f *fakeOrgStore) ListProjectMembers(context.Context, string) ([]*domain.ProjectMember, error) {
	return nil, nil
}
func (f *fakeOrgStore) GetProjectMember(context.Context, string, string) (*domain.ProjectMember, error) {
	return nil, nil
}
func (f *fakeOrgStore) CreatePendingInvite(context.Context, *domain.PendingInvite) error { return nil }
func (f *fakeOrgStore) FindPendingInvitesByEmail(context.Context, string) ([]*domain.PendingInvite, error) {
	return nil, nil
}
func (f *fakeOrgStore) DeletePendingInvite(context.Context, int64) error { return nil }
func (f *fakeOrgStore) ListPendingInvites(context.Context, string) ([]*domain.PendingInvite, error) {
	return nil, nil
}

// projectAccessRequest builds a request routed to {projectId} with an optional
// authenticated user attached.
func projectAccessRequest(projectID string, user *domain.User) *http.Request {
	r := httptest.NewRequest("GET", "/api/provision/"+projectID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("projectId", projectID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	if user != nil {
		r = r.WithContext(auth.SetUser(r.Context(), user))
	}
	return r
}

func runProjectAccess(t *testing.T, inst *fakeInstanceStore, org *fakeOrgStore, r *http.Request) int {
	t.Helper()
	called := false
	h := RequireProjectAccess(inst, org)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	r := httptest.NewRequest("GET", "/", nil) // no {projectId} param
	code := runProjectAccess(t, &fakeInstanceStore{}, &fakeOrgStore{}, r)
	if code != http.StatusOK {
		t.Errorf("no projectId should pass through, got %d", code)
	}
}

func TestRequireProjectAccess_Unauthenticated(t *testing.T) {
	r := projectAccessRequest("proj-1", nil)
	code := runProjectAccess(t, &fakeInstanceStore{}, &fakeOrgStore{}, r)
	if code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", code)
	}
}

func TestRequireProjectAccess_AdminBypass(t *testing.T) {
	r := projectAccessRequest("proj-1", &domain.User{ID: "a", Role: "platform_admin"})
	// inst store would 404, but admin bypasses before the lookup.
	code := runProjectAccess(t, &fakeInstanceStore{}, &fakeOrgStore{}, r)
	if code != http.StatusOK {
		t.Errorf("admin should bypass, got %d", code)
	}
}

func TestRequireProjectAccess_ProjectNotFound(t *testing.T) {
	r := projectAccessRequest("ghost", &domain.User{ID: "u", Role: "user"})
	code := runProjectAccess(t, &fakeInstanceStore{inst: nil}, &fakeOrgStore{}, r)
	if code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", code)
	}
}

func TestRequireProjectAccess_NoOrgFailsClosed(t *testing.T) {
	r := projectAccessRequest("proj-1", &domain.User{ID: "u", Role: "user"})
	inst := &fakeInstanceStore{inst: &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: ""}}
	code := runProjectAccess(t, inst, &fakeOrgStore{}, r)
	if code != http.StatusForbidden {
		t.Errorf("expected 403 for empty org, got %d", code)
	}
}

func TestRequireProjectAccess_NonMemberForbidden(t *testing.T) {
	r := projectAccessRequest("proj-1", &domain.User{ID: "stranger", Role: "user"})
	inst := &fakeInstanceStore{inst: &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org-1"}}
	code := runProjectAccess(t, inst, &fakeOrgStore{member: nil}, r)
	if code != http.StatusForbidden {
		t.Errorf("expected 403 for non-member, got %d", code)
	}
}

func TestRequireProjectAccess_MemberGranted(t *testing.T) {
	memberID := testutil.FixturePassword("member")
	r := projectAccessRequest("proj-1", &domain.User{ID: memberID, Role: "user"})
	inst := &fakeInstanceStore{inst: &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org-1"}}
	org := &fakeOrgStore{member: &domain.OrgMember{OrgID: "org-1", UserID: memberID, Role: "developer"}}
	code := runProjectAccess(t, inst, org, r)
	if code != http.StatusOK {
		t.Errorf("expected 200 for org member, got %d", code)
	}
}
