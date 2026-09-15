package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

// EXC-349 regression: the /api/schema/{projectId} mount must enforce project
// ownership, and it must do so where {projectId} is bound (a guard applied one
// level too high sees an empty projectId and becomes a silent no-op). These
// tests pin the mount SHAPE the fix relies on: RequireProjectAccess sits under
// the {projectId} segment, so a non-member is rejected before reaching any
// schema handler, while an org member (and a platform_admin) pass through.

// schemaMount mirrors the fixed /api/schema/{projectId} wiring with a sentinel
// leaf that records whether the guarded handler was reached.
func schemaMount(inst *domain.DatabaseInstance, member *domain.OrgMember, reached *bool) chi.Router {
	instStore := fakestore.NewInstances()
	instStore.Save(inst)
	orgStore := fakestore.NewOrgs()
	if member != nil {
		orgStore.AddMember(member.OrgID, member.UserID, member.Role)
	}
	r := chi.NewRouter()
	r.Route("/api/schema/{projectId}", func(r chi.Router) {
		r.Use(RequireProjectAccess(instStore, orgStore))
		leaf := func(w http.ResponseWriter, _ *http.Request) {
			*reached = true
			w.WriteHeader(http.StatusOK)
		}
		// Representative slice of the 37-endpoint surface: read, arbitrary
		// SQL, DDL, and RLS-policy read — the ones the PoC abused.
		r.Get("/tables", leaf)
		r.Post("/query", leaf)
		r.Post("/ddl", leaf)
		r.Get("/policies", leaf)
	})
	return r
}

func do(t *testing.T, router chi.Router, method, path string, user *domain.User) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if user != nil {
		req = req.WithContext(auth.SetUser(req.Context(), user))
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code
}

func TestSchemaMount_NonMemberBlockedOnEveryVerb(t *testing.T) {
	victim := &domain.DatabaseInstance{ProjectID: "proj-victim", OrgID: "org-victim"}
	// mallory: authenticated "user" with no membership in org-victim.
	mallory := &domain.User{ID: "u-mallory", Role: "user"}
	cases := []struct {
		method, path string
	}{
		{"GET", "/api/schema/proj-victim/tables"},
		{"POST", "/api/schema/proj-victim/query"},
		{"POST", "/api/schema/proj-victim/ddl"},
		{"GET", "/api/schema/proj-victim/policies"},
	}
	for _, c := range cases {
		reached := false
		router := schemaMount(victim, nil /* not a member */, &reached)
		code := do(t, router, c.method, c.path, mallory)
		if code != http.StatusNotFound {
			t.Errorf("%s %s: got %d, want 404 (non-member must be blocked without confirming the project exists)", c.method, c.path, code)
		}
		if reached {
			t.Errorf("%s %s: guarded handler was reached by a non-member", c.method, c.path)
		}
	}
}

func TestSchemaMount_Unauthenticated401(t *testing.T) {
	victim := &domain.DatabaseInstance{ProjectID: "proj-victim", OrgID: "org-victim"}
	reached := false
	router := schemaMount(victim, nil, &reached)
	if code := do(t, router, "GET", "/api/schema/proj-victim/tables", nil); code != http.StatusUnauthorized {
		t.Errorf("no user: got %d, want 401", code)
	}
	if reached {
		t.Error("unauthenticated request reached the guarded handler")
	}
}

func TestSchemaMount_MemberPassesThrough(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "proj-own", OrgID: "org-own"}
	member := &domain.OrgMember{OrgID: "org-own", UserID: "u-owner", Role: "owner"}
	owner := &domain.User{ID: "u-owner", Role: "user"}
	reached := false
	router := schemaMount(inst, member, &reached)
	if code := do(t, router, "GET", "/api/schema/proj-own/tables", owner); code != http.StatusOK {
		t.Errorf("org member: got %d, want 200 (guard must not over-block the owner)", code)
	}
	if !reached {
		t.Error("org member did not reach the guarded handler")
	}
}

func TestSchemaMount_PlatformAdminBypasses(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "proj-any", OrgID: "org-any"}
	admin := &domain.User{ID: "u-admin", Role: "platform_admin"}
	reached := false
	// No membership row: admin must still pass via the platform bypass.
	router := schemaMount(inst, nil, &reached)
	if code := do(t, router, "GET", "/api/schema/proj-any/tables", admin); code != http.StatusOK {
		t.Errorf("platform_admin: got %d, want 200 (support/incident bypass must survive)", code)
	}
	if !reached {
		t.Error("platform_admin did not reach the guarded handler")
	}
}
