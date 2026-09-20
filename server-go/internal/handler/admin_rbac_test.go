package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

// newAdminHForRBAC builds an AdminHandler with in-memory deps so that requests
// which pass the permission gate reach a working handler (not a nil panic),
// letting the test distinguish "403 blocked by RBAC" from "allowed past gate".
func newAdminHForRBAC(t *testing.T) *AdminHandler {
	t.Helper()
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-x": {ProjectID: "proj-x", OrgID: "org-1", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Namespace: "ctr-x"},
	}}
	provSvc := newDockerProvSvc(t, store)
	orgStore := &adminOrgStore{org: &domain.Org{ID: "org-1", Slug: "acme"}}
	return NewAdminHandler(provSvc, store, orgStore, &captureAudit{}, nil, "", nil)
}

// adminRouterAs mounts the admin routes behind a middleware that injects an
// authenticated user with the given platform role, so RequirePermission runs
// against a real role.
func adminRouterAs(role string, h *AdminHandler) http.Handler {
	return adminRouterWith(role, &domain.AccessToken{Scopes: auth.ScopeSession}, h)
}

// adminRouterWith is adminRouterAs with an explicit credential, so a test can
// separate "what this role may do" from "what this credential may do".
func adminRouterWith(role string, token *domain.AccessToken, h *AdminHandler) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "u1", Role: role, Active: true})
			ctx = auth.SetToken(ctx, token)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/admin", h.Routes)
	return r
}

// TestAdmin_DestructiveRoutesRBAC pins the RBAC on the admin surface (SEC-C6):
// a read permission (view_any) must NOT authorize destructive operations. A
// platform_viewer can list but never force-drop a project or revoke an org; a
// platform_operator can force-drop stuck projects but not revoke a whole org
// (a cross-tenant governance action reserved for platform_admin).
func TestAdmin_DestructiveRoutesRBAC(t *testing.T) {
	cases := []struct {
		name          string
		role          string
		method        string
		path          string
		wantForbidden bool
	}{
		{"viewer cannot force-drop project", "platform_viewer", "DELETE", "/api/admin/projects/proj-x", true},
		{"viewer cannot revoke org", "platform_viewer", "DELETE", "/api/admin/orgs/org-1?cascade=true", true},
		{"operator cannot revoke org", "platform_operator", "DELETE", "/api/admin/orgs/org-1?cascade=true", true},
		{"viewer can list projects", "platform_viewer", "GET", "/api/admin/projects", false},
		{"operator can force-drop project", "platform_operator", "DELETE", "/api/admin/projects/proj-x", false},
		{"admin can revoke org", "platform_admin", "DELETE", "/api/admin/orgs/org-1?cascade=true", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newAdminHForRBAC(t)
			r := adminRouterAs(tc.role, h)
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if tc.wantForbidden && w.Code != http.StatusForbidden {
				t.Errorf("got %d, want 403 (route must reject %s)", w.Code, tc.role)
			}
			if !tc.wantForbidden && w.Code == http.StatusForbidden {
				t.Errorf("got 403, %s should pass the permission gate", tc.role)
			}
		})
	}
}

// EXC-418: the force drop is the one admin route that destroys a tenant it
// never binds the caller to. A credential narrowed to one project must not
// reach it — the role is wide, the credential is not.
func TestAdmin_ForceDropRefusesANarrowedCredential(t *testing.T) {
	narrowed := []struct {
		name  string
		token *domain.AccessToken
	}{
		{"project-bound PAT", &domain.AccessToken{ProjectID: "proj-other"}},
		{"scope-limited PAT", &domain.AccessToken{Scopes: auth.ScopeWrite}},
		{"no credential at all", nil},
	}
	for _, tc := range narrowed {
		t.Run(tc.name, func(t *testing.T) {
			r := adminRouterWith("platform_admin", tc.token, newAdminHForRBAC(t))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/admin/projects/proj-x", nil))
			if w.Code != http.StatusForbidden {
				t.Fatalf("got %d, want 403", w.Code)
			}
		})
	}
}
