package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

func patchOrgAs(t *testing.T, user *domain.User, body string) *httptest.ResponseRecorder {
	t.Helper()
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org-plan", domain.Free)
	orgs.AddMember("org-plan", "owner-1", domain.OrgRoleOwner)
	h := NewOrgHandler(orgs, nil)
	r := chi.NewRouter()
	r.Patch("/orgs/{orgId}", h.UpdateOrg)

	req := httptest.NewRequest("PATCH", "/orgs/org-plan", strings.NewReader(body))
	req = req.WithContext(auth.SetUser(req.Context(), user))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestUpdateOrgTier_RefusedForTheOrgsOwnOwner(t *testing.T) {
	owner := &domain.User{ID: "owner-1", Role: "user", Active: true}
	if w := patchOrgAs(t, owner, `{"tier":"ENTERPRISE"}`); w.Code != http.StatusForbidden {
		t.Fatalf("owner tier change: got %d, want 403; body %s", w.Code, w.Body.String())
	}
	if w := patchOrgAs(t, owner, `{"name":"Renamed"}`); w.Code != http.StatusOK {
		t.Fatalf("owner rename: got %d, want 200; body %s", w.Code, w.Body.String())
	}
}

func TestUpdateOrgTier_AllowedForThePlatform(t *testing.T) {
	admin := &domain.User{ID: "admin-1", Role: "platform_admin", Active: true}
	w := patchOrgAs(t, admin, `{"tier":"ENTERPRISE"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tier":"ENTERPRISE"`) {
		t.Fatalf("platform tier change: got %d %s", w.Code, w.Body.String())
	}
}
