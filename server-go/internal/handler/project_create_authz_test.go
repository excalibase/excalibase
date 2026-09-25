package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const (
	createAuthzOrg     = "org-target"
	createAuthzCaller  = "caller-1"
	createAuthzForeign = "org-foreign"
)

// provisionAs posts a well-formed project creation into createAuthzOrg as a
// dashboard user holding orgRole there ("" for no membership at all), so the
// request reaches the org permission check rather than stopping at the body.
func provisionAs(t *testing.T, memberships map[string]string) int {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	orgs := fakestore.NewOrgs()
	for orgID, role := range memberships {
		orgs.AddMember(orgID, createAuthzCaller, role)
	}
	h := NewProvisioningHandler(service.NewProvisioningService(store, provisioner.NewFactory(), nil), orgs)
	r := chi.NewRouter()
	r.Post("/api/provision/", h.Provision)

	body := `{"projectName":"p","orgId":"` + createAuthzOrg + `","databaseType":"POSTGRESQL","tier":"FREE","postgresVersion":"17"}`
	req := httptest.NewRequest("POST", "/api/provision/", strings.NewReader(body))
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: createAuthzCaller, Role: "user", Active: true}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestProvisionRefusesCallersWithoutTheCreateProjectOrgPermission(t *testing.T) {
	for name, memberships := range map[string]map[string]string{
		"developer":        {createAuthzOrg: domain.OrgRoleDeveloper},
		"viewer":           {createAuthzOrg: domain.OrgRoleViewer},
		"non-member":       {},
		"owner of another": {createAuthzForeign: domain.OrgRoleOwner},
	} {
		t.Run(name, func(t *testing.T) {
			if code := provisionAs(t, memberships); code != http.StatusForbidden {
				t.Errorf("got %d, want 403", code)
			}
		})
	}
}

// The refusal is the role check and not a blanket one: an owner or admin of
// the target org gets past it and fails later, at the empty provisioner.
func TestProvisionLetsOrgOwnersAndAdminsPastThePermissionCheck(t *testing.T) {
	for _, role := range []string{domain.OrgRoleOwner, domain.OrgRoleAdmin} {
		t.Run(role, func(t *testing.T) {
			if code := provisionAs(t, map[string]string{createAuthzOrg: role}); code == http.StatusForbidden {
				t.Errorf("an org %s was refused project creation", role)
			}
		})
	}
}
