//go:build integration

package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

// EXC-524: the plan's app count comes from the migrated tier_configs row, and
// the real Postgres app store enforces it. FREE admits two apps, not three; an
// admin's edit takes effect on the next create.
func TestAppLimit_FreePlanFromThePlatformDatabase(t *testing.T) {
	store := pgtest.New(t)
	ctx := context.Background()
	const org, project, owner = "org-applimit", "proj-applimit", "owner-applimit"
	user := &domain.User{ID: owner, Username: testutil.FixtureToken("applimit"), Email: "applimit@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := store.CreateOrg(ctx, &domain.Org{ID: org, Name: org, Slug: org, Tier: domain.Free, OwnerID: owner}); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: project, OrgID: org, DBType: domain.PostgreSQL, Tier: domain.Free, Status: "ACTIVE"}); err != nil {
		t.Fatalf("Create project: %v", err)
	}
	prov := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
	prov.SetTierStore(store)
	plans := service.NewOrgPlanTiers(store, store)
	h := NewAppHandler(apphost.NewPostgresAppStore(store.DB()), newFakeSources(), testAppRoute)
	h.SetAppLimits(service.NewAppLimits(plans, prov))
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps", func(r chi.Router) { h.Routes(r) })

	create := func(name string) int {
		body := validAppBody()
		body["name"] = name
		delete(body, "env")
		return doAppRequest(t, r, http.MethodPost, "/api/projects/"+project+"/apps/", body).Code
	}
	for _, name := range []string{"web", "redis"} {
		if code := create(name); code != http.StatusCreated {
			t.Fatalf("%s on FREE: %d, want 201", name, code)
		}
	}
	if code := create("worker"); code != http.StatusConflict {
		t.Fatalf("a third app on FREE: %d, want 409", code)
	}

	free, _, err := store.GetTierConfig(ctx, domain.Free)
	if err != nil {
		t.Fatalf("GetTierConfig: %v", err)
	}
	free.MaxApps = 3
	if err := store.UpsertTierConfig(ctx, domain.Free, free); err != nil {
		t.Fatalf("UpsertTierConfig: %v", err)
	}
	if code := create("worker"); code != http.StatusCreated {
		t.Fatalf("after an admin raised FREE to 3: %d, want 201", code)
	}
}
