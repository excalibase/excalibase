//go:build integration

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

const (
	suspendedProject   = "susp-proj-a"
	suspendedApp       = "susp-app-a"
	suspendedNamespace = "excalibase-susp-org-a-susp-proj-a"
	carolID            = "e2e-carol"
	platformAdminID    = "e2e-platform-admin"
)

// suspendedRouter mirrors the production apps mount, with the real service,
// guard and Postgres store.
func suspendedRouter(t *testing.T, store *pgstore.Store) chi.Router {
	t.Helper()
	kube := k8s.NewMockClient()
	kube.Capacity = k8s.ClusterCapacity{AllocatableCPUMilli: 64000, AllocatableMemBytes: 256 << 30,
		Nodes: []k8s.NodeCapacity{{Name: "n1", AllocatableCPUMilli: 64000, AllocatableMemBytes: 256 << 30}}}
	kube.PausedReplicas = 1
	prov := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
	prov.SetTierStore(store)
	deploys := service.NewAppDeployService(apphost.NewPostgresAppStore(store.DB()), apphost.NewPostgresDeployStore(store.DB()),
		kube, store, nil, k8s.AppRenderOptions{RuntimeClass: "gvisor", Route: k8s.AppRouteOptions{
			Domain: "apps.example.com", IngressClass: "haproxy", IngressFromNamespace: "haproxy-controller"}})
	deploys.SetPlanTiers(service.NewOrgPlanTiers(store, store))
	deploys.SetNamespaceQuotaTiers(prov)
	deploys.SetOrgGuard(orgguard.New(store, pgstore.NewAdminConsole(store.DB()).OwnerSuspendedOrgs))
	h := NewAppDeployHandler(deploys)
	h.SetFeatures(features.NewStatic(features.All()...))

	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(store))
	r.Route("/api/projects/{projectId}/apps/{appId}", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, store))
		dev := custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, store)
		r.With(dev).Post("/resume", h.Resume)
		r.With(dev).Post("/deploy", h.Deploy)
	})
	return r
}

// suspendedSeed: alice owns org A and is abuse-suspended; carol is a second
// active Developer in it; the org's app is stopped.
func suspendedSeed(t *testing.T, store *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	e2eSeed(t, store)
	for _, u := range []*domain.User{
		{ID: carolID, Username: testutil.FixtureToken("e2e-carol"), Email: "e2e-carol@test.com", Role: "user"},
		{ID: platformAdminID, Username: testutil.FixtureToken("e2e-padmin"), Email: "e2e-padmin@test.com", Role: "platform_admin"},
	} {
		u.PasswordHash, u.Active = testutil.FixturePasswordHash(), true
		if err := store.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}
	if err := store.AddOrgMember(ctx, &domain.OrgMember{OrgID: e2eOrgA, UserID: carolID, Role: domain.OrgRoleDeveloper}); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: suspendedProject, OrgID: e2eOrgA, OwnerID: testAliceID,
		DBType: domain.PostgreSQL, Tier: domain.Free, Status: "ACTIVE", Namespace: suspendedNamespace}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	app := &apphost.App{ID: suspendedApp, ProjectID: suspendedProject, Name: "web", Image: "ghcr.io/acme/web:v1",
		Env: []apphost.EnvVar{}, Port: 8080, Replicas: 1, Tier: domain.Free, Status: apphost.StatusStopped}
	if err := apphost.NewPostgresAppStore(store.DB()).Create(app, 2); err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO abuse_suspensions (user_id, email, reason) VALUES ($1, 'e2e-alice@test.com', 'abuse')`, testAliceID); err != nil {
		t.Fatalf("suspend alice: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE users SET active = false WHERE id = $1`, testAliceID); err != nil {
		t.Fatalf("deactivate alice: %v", err)
	}
}

func appStatus(t *testing.T, store *pgstore.Store) string {
	t.Helper()
	app, err := apphost.NewPostgresAppStore(store.DB()).Get(suspendedProject, suspendedApp)
	if err != nil {
		t.Fatalf("read app: %v", err)
	}
	return app.Status
}

func TestResumeRoute_ADeveloperOfASuspendedOwnersOrgIsRefused(t *testing.T) {
	store := pgtest.New(t)
	suspendedSeed(t, store)
	r := suspendedRouter(t, store)
	token := e2eIssueToken(t, store, "carol-write", carolID, domain.AccessToken{Scopes: auth.ScopeWrite})
	path := "/api/projects/" + suspendedProject + "/apps/" + suspendedApp + "/resume"

	for name, prefer := range map[string]string{"waiting": "", "respond-async": "respond-async"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if prefer != "" {
			req.Header.Set("Prefer", prefer)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "This organisation is suspended. Contact support.") {
			t.Fatalf("%s: %d %s, want 403 with the suspension message", name, rec.Code, rec.Body.String())
		}
		if got := appStatus(t, store); got != apphost.StatusStopped {
			t.Fatalf("%s: app status = %q, the refused resume must leave it stopped", name, got)
		}
	}

	deploy := ciCall(t, r, http.MethodPost, "/api/projects/"+suspendedProject+"/apps/"+suspendedApp+"/deploy", token, "{}")
	if deploy.Code != http.StatusForbidden {
		t.Fatalf("deploy: %d %s, want 403", deploy.Code, deploy.Body.String())
	}
}

func TestResumeRoute_APlatformAdminMayResume(t *testing.T) {
	store := pgtest.New(t)
	suspendedSeed(t, store)
	r := suspendedRouter(t, store)
	token := e2eIssueToken(t, store, "platform-admin", platformAdminID,
		domain.AccessToken{Scopes: auth.ScopeWrite, Permissions: []string{"apps:resume"}})

	rec := ciCall(t, r, http.MethodPost, "/api/projects/"+suspendedProject+"/apps/"+suspendedApp+"/resume", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("platform admin resume: %d %s", rec.Code, rec.Body.String())
	}
	if got := appStatus(t, store); got != apphost.StatusRunning {
		t.Fatalf("app status = %q, want running", got)
	}
}

func TestResumeRoute_AnOrgWithoutASuspensionIsUnaffected(t *testing.T) {
	store := pgtest.New(t)
	suspendedSeed(t, store)
	if _, err := store.DB().ExecContext(context.Background(), `DELETE FROM abuse_suspensions`); err != nil {
		t.Fatal(err)
	}
	r := suspendedRouter(t, store)
	token := e2eIssueToken(t, store, "carol-write", carolID, domain.AccessToken{Scopes: auth.ScopeWrite})

	rec := ciCall(t, r, http.MethodPost, "/api/projects/"+suspendedProject+"/apps/"+suspendedApp+"/resume", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
	}
	if got := appStatus(t, store); got != apphost.StatusRunning {
		t.Fatalf("app status = %q, want running", got)
	}
}
