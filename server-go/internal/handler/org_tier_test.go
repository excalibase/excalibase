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
	h.SetNodePlacement(&fakeNodes{nodes: 5})
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

func consoleTier(t *testing.T, nodes *fakeNodes, user *domain.User, orgID, body string) (*httptest.ResponseRecorder, *captureAudit) {
	t.Helper()
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org-plan", domain.Free)
	h := NewOrgHandler(orgs, nil)
	if nodes != nil {
		h.SetNodePlacement(nodes)
	}
	audit := &captureAudit{}
	h.SetAudit(audit)
	r := chi.NewRouter()
	r.Patch("/orgs/{orgId}/tier", h.SetOrgTier)
	req := httptest.NewRequest("PATCH", "/orgs/"+orgID+"/tier", strings.NewReader(body))
	req = req.WithContext(auth.SetUser(req.Context(), user))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w, audit
}

var consoleAdmin = &domain.User{ID: "admin-1", Role: "platform_admin", Active: true}

// The platform sets a plan on an org it does not belong to through the
// console: a written reason is required and the change is audited.
func TestSetOrgTier_ByThePlatformWithAReasonIsAudited(t *testing.T) {
	w, audit := consoleTier(t, &fakeNodes{nodes: 5}, consoleAdmin, "org-plan", `{"tier":"ENTERPRISE","reason":"agreed upgrade, invoice 42"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tier":"ENTERPRISE"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != "admin.set_org_tier" || !strings.Contains(audit.entries[0].Details, "agreed upgrade, invoice 42") {
		t.Fatalf("audit = %+v", audit.entries)
	}
}

func TestSetOrgTier_RefusesWithoutAReasonOrWithAnInvalidTier(t *testing.T) {
	for _, body := range []string{`{"tier":"ENTERPRISE"}`, `{"tier":"ENTERPRISE","reason":"  "}`, `{"tier":"SUPER_PLAN","reason":"because"}`} {
		if w, _ := consoleTier(t, &fakeNodes{nodes: 5}, consoleAdmin, "org-plan", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", body, w.Code)
		}
	}
	if w, _ := consoleTier(t, &fakeNodes{nodes: 5}, consoleAdmin, "ghost", `{"tier":"ENTERPRISE","reason":"because"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown org: got %d, want 404", w.Code)
	}
}

// A foreign platform admin cannot change a plan through the org route.
func TestUpdateOrgTier_NotForAForeignPlatformAdmin(t *testing.T) {
	if w := patchOrgAs(t, consoleAdmin, `{"tier":"ENTERPRISE"}`); w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404; body %s", w.Code, w.Body.String())
	}
}
