//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

const (
	ciProject   = "ci-proj-a"
	ciApp       = "ci-app-a"
	ciNamespace = "excalibase-ci-org-a-ci-proj-a"
)

var ciDigest = "sha256:" + strings.Repeat("5e", 32)

// fixedDigest answers every image with one digest, the way a registry would for one push.
type fixedDigest struct{ asked []string }

func (f *fixedDigest) Resolve(_ context.Context, image string, _ *apphost.RegistryCredential) (string, error) {
	f.asked = append(f.asked, image)
	return ciDigest, nil
}

// ciRouter mirrors the production apps mount over the real Postgres store.
func ciRouter(t *testing.T, store *pgstore.Store, resolver service.ImageResolver) chi.Router {
	t.Helper()
	kube := k8s.NewMockClient()
	kube.Capacity = k8s.ClusterCapacity{AllocatableCPUMilli: 64000, AllocatableMemBytes: 256 << 30,
		Nodes: []k8s.NodeCapacity{{Name: "n1", AllocatableCPUMilli: 64000, AllocatableMemBytes: 256 << 30}}}
	prov := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
	prov.SetTierStore(store)
	deploys := service.NewAppDeployService(apphost.NewPostgresAppStore(store.DB()), apphost.NewPostgresDeployStore(store.DB()),
		kube, store, nil, k8s.AppRenderOptions{RuntimeClass: "gvisor", Route: k8s.AppRouteOptions{
			Domain: "apps.example.com", IngressClass: "haproxy", IngressFromNamespace: "haproxy-controller"}})
	deploys.SetPlanTiers(service.NewOrgPlanTiers(store, store))
	deploys.SetNamespaceQuotaTiers(prov)
	deploys.SetImageResolver(resolver)
	h := NewAppDeployHandler(deploys)
	h.SetFeatures(features.NewStatic(features.Pipeline))
	h.waitPoll = 20 * time.Millisecond

	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(store))
	r.Route("/api/projects/{projectId}/apps/{appId}", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, store))
		dev := custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, store)
		r.With(dev).Post("/deploy", h.Deploy)
		r.Get("/deploys/{deployId}", h.GetDeploy)
	})
	return r
}

func ciSeed(t *testing.T, store *pgstore.Store) {
	t.Helper()
	e2eSeed(t, store)
	if err := store.Create(&domain.DatabaseInstance{ProjectID: ciProject, OrgID: e2eOrgA, OwnerID: testAliceID,
		DBType: domain.PostgreSQL, Tier: domain.Free, Status: "ACTIVE", Namespace: ciNamespace}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	app := &apphost.App{ID: ciApp, ProjectID: ciProject, Name: "web", Image: "ghcr.io/acme/web:v1",
		Env: []apphost.EnvVar{}, Port: 8080, Replicas: 1, Tier: domain.Free, Status: apphost.StatusCreated}
	if err := apphost.NewPostgresAppStore(store.DB()).Create(app, 2); err != nil {
		t.Fatalf("create app: %v", err)
	}
}

func ciCall(t *testing.T, r chi.Router, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// EXC-543: a CI job holding a write token bound to the project deploys a
// pushed image by tag, gets back the deploy, and polls it until it is healthy.
func TestCIDeploy_AProjectBoundWriteTokenDeploysAndPolls(t *testing.T) {
	store := pgtest.New(t)
	ciSeed(t, store)
	resolver := &fixedDigest{}
	r := ciRouter(t, store, resolver)
	deployPath := "/api/projects/" + ciProject + "/apps/" + ciApp + "/deploy"
	body := `{"image":"ghcr.io/acme/web:main","commitSha":"9fceb02d0ae598e95dc970b74767f19372d61af8"}`

	writeToken := e2eIssueToken(t, store, "ci-write", testAliceID, domain.AccessToken{Scopes: auth.ScopeWrite, ProjectID: ciProject})
	rec := ciCall(t, r, http.MethodPost, deployPath, writeToken, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body.String())
	}
	var started struct {
		ID, Status, URL, Digest, Image, Source, CommitSHA string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.ID == "" || started.URL == "" || started.Digest != ciDigest || started.Image != "ghcr.io/acme/web@"+ciDigest ||
		started.Source != apphost.DeploySourceAPI {
		t.Fatalf("deploy answer = %+v", started)
	}

	statusPath := "/api/projects/" + ciProject + "/apps/" + ciApp + "/deploys/" + started.ID
	deadline := time.Now().Add(10 * time.Second)
	status := started.Status
	for status != apphost.DeployStatusSucceeded && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		poll := ciCall(t, r, http.MethodGet, statusPath, writeToken, "")
		if poll.Code != http.StatusOK {
			t.Fatalf("poll: %d %s", poll.Code, poll.Body.String())
		}
		var polled struct{ Status, CommitSHA string }
		_ = json.Unmarshal(poll.Body.Bytes(), &polled)
		status = polled.Status
	}
	if status != apphost.DeployStatusSucceeded {
		t.Fatalf("the deploy never finished: %s", status)
	}

	app, err := apphost.NewPostgresAppStore(store.DB()).Get(ciProject, ciApp)
	if err != nil || app.Image != "ghcr.io/acme/web:main" || app.ResolvedDigest != ciDigest {
		t.Fatalf("app = %+v, %v", app, err)
	}
}

// EXC-571: with ?wait=true the deploy call itself answers once the deploy is live.
func TestCIDeploy_AWaitedDeployAnswersLive(t *testing.T) {
	store := pgtest.New(t)
	ciSeed(t, store)
	r := ciRouter(t, store, &fixedDigest{})
	writeToken := e2eIssueToken(t, store, "ci-write", testAliceID, domain.AccessToken{Scopes: auth.ScopeWrite, ProjectID: ciProject})

	rec := ciCall(t, r, http.MethodPost, "/api/projects/"+ciProject+"/apps/"+ciApp+"/deploy?wait=true&timeout=30", writeToken,
		`{"image":"ghcr.io/acme/web:main","commitSha":"9fceb02d0ae598e95dc970b74767f19372d61af8"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("waited deploy: %d %s", rec.Code, rec.Body.String())
	}
	var live struct{ ID, Status, URL string }
	if err := json.Unmarshal(rec.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if live.ID == "" || live.Status != apphost.DeployStatusSucceeded || live.URL == "" {
		t.Fatalf("waited deploy answer = %+v", live)
	}
}

// A token is a door to one project and one level of access: a read token
// polls but never deploys, and a token bound elsewhere sees nothing.
func TestCIDeploy_TokensOutsideTheirGrantAreRefused(t *testing.T) {
	store := pgtest.New(t)
	ciSeed(t, store)
	resolver := &fixedDigest{}
	r := ciRouter(t, store, resolver)
	deployPath := "/api/projects/" + ciProject + "/apps/" + ciApp + "/deploy"
	body := `{"image":"ghcr.io/acme/web:main"}`

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"a read token", e2eIssueToken(t, store, "ci-read", testAliceID, domain.AccessToken{Scopes: auth.ScopeRead, ProjectID: ciProject}), http.StatusForbidden},
		{"a write token bound to another project", e2eIssueToken(t, store, "ci-other", testAliceID, domain.AccessToken{Scopes: auth.ScopeWrite, ProjectID: e2eProjectA}), http.StatusNotFound},
		{"another organisation's token", e2eIssueToken(t, store, "ci-bob", testBobID, domain.AccessToken{Scopes: auth.ScopeWrite}), http.StatusNotFound},
		{"no token", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		if rec := ciCall(t, r, http.MethodPost, deployPath, tc.token, body); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
	if len(resolver.asked) != 0 {
		t.Fatalf("a refused caller made the platform ask a registry: %v", resolver.asked)
	}
	readToken := e2eIssueToken(t, store, "ci-read-poll", testAliceID, domain.AccessToken{Scopes: auth.ScopeRead, ProjectID: ciProject})
	if rec := ciCall(t, r, http.MethodGet, "/api/projects/"+ciProject+"/apps/"+ciApp+"/deploys/nope", readToken, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a read token polls (an unknown deploy is 404): %d", rec.Code)
	}
}
