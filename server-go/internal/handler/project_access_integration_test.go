//go:build integration

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

const (
	e2eProjectA = "e2e-proj-a"
	e2eProjectB = "e2e-proj-b"
	e2eOrgA     = "e2e-org-a"
	e2eOrgB     = "e2e-org-b"
	e2eStatusA  = "/api/provision/" + e2eProjectA
)

// e2eIssueToken persists a PAT for userID in the real store and returns the
// raw bearer value the request must send.
func e2eIssueToken(t *testing.T, store *pgstore.Store, name, userID string, tok domain.AccessToken) string {
	t.Helper()
	raw := testutil.FixtureToken(name)
	tok.TokenHash = auth.HashToken(raw)
	tok.TokenPrefix = auth.TokenPrefix(raw)
	tok.UserID = userID
	tok.Name = name
	if err := store.CreateToken(context.Background(), &tok); err != nil {
		t.Fatalf("CreateToken %s: %v", name, err)
	}
	return raw
}

// e2eSeed creates alice (owner of org A), bob (developer in org B) and one
// project per org, all in Postgres.
func e2eSeed(t *testing.T, store *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	users := []*domain.User{
		{ID: testAliceID, Username: testutil.FixtureToken("e2e-alice"), Email: "e2e-alice@test.com"},
		{ID: testBobID, Username: testutil.FixtureToken("e2e-bob"), Email: "e2e-bob@test.com"},
	}
	for _, u := range users {
		u.PasswordHash, u.Role, u.Active = testutil.FixturePasswordHash(), "user", true
		if err := store.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}
	orgs := []struct{ id, owner, member, role, project string }{
		{e2eOrgA, testAliceID, testAliceID, domain.OrgRoleOwner, e2eProjectA},
		{e2eOrgB, testBobID, testBobID, domain.OrgRoleDeveloper, e2eProjectB},
	}
	for _, o := range orgs {
		if err := store.CreateOrg(ctx, &domain.Org{ID: o.id, Name: o.id, Slug: o.id, Tier: domain.Free, OwnerID: o.owner}); err != nil {
			t.Fatalf("CreateOrg: %v", err)
		}
		if err := store.AddOrgMember(ctx, &domain.OrgMember{OrgID: o.id, UserID: o.member, Role: o.role}); err != nil {
			t.Fatalf("AddOrgMember: %v", err)
		}
		if err := store.Create(&domain.DatabaseInstance{ProjectID: o.project, OrgID: o.id, OwnerID: o.owner, DBType: domain.PostgreSQL, Tier: domain.Free, Status: "ACTIVE"}); err != nil {
			t.Fatalf("Save instance: %v", err)
		}
	}
}

// e2eRouter mirrors the production /api/provision/{projectId} mount over the
// real Postgres store: ExtractAuth → RequireAuth → TenantContext →
// RequireProjectAccess → GetStatus.
func e2eRouter(store *pgstore.Store) chi.Router {
	provSvc := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
	provH := NewProvisioningHandler(provSvc, store)
	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(store))
	r.Route("/api/provision/{projectId}", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Use(custommw.TenantContext)
		r.Use(custommw.RequireProjectAccess(store, store))
		r.Get("/", provH.GetStatus)
	})
	return r
}

// TestProjectAccess_CrossOrgRefusedEndToEnd proves, against a real Postgres
// platform store, that a valid session for org B cannot read org A's project
// through the path, that a PAT never leaves the project it is bound to, and
// that a binding cannot grant membership the user does not have.
func TestProjectAccess_CrossOrgRefusedEndToEnd(t *testing.T) {
	store := pgtest.New(t)
	e2eSeed(t, store)
	r := e2eRouter(store)

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"owner of org A", e2eIssueToken(t, store, "alice-session", testAliceID, domain.AccessToken{Scopes: auth.ScopeSession}), http.StatusOK},
		{"member of org B", e2eIssueToken(t, store, "bob-session", testBobID, domain.AccessToken{Scopes: auth.ScopeSession}), http.StatusNotFound},
		{"alice PAT bound to project B", e2eIssueToken(t, store, "alice-bound-b", testAliceID, domain.AccessToken{ProjectID: e2eProjectB}), http.StatusNotFound},
		{"alice PAT bound to project A", e2eIssueToken(t, store, "alice-bound-a", testAliceID, domain.AccessToken{ProjectID: e2eProjectA, Scopes: auth.ScopeRead}), http.StatusOK},
		{"bob PAT bound to project A", e2eIssueToken(t, store, "bob-bound-a", testBobID, domain.AccessToken{ProjectID: e2eProjectA}), http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, e2eStatusA, nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
