//go:build integration

package sites_test

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// A deploy names who made it by username: the stored actor is an opaque user id
// that means nothing to the people reading the history (EXC-353).
func TestPostgresSiteDeploysNameTheirDeployer(t *testing.T) {
	platform := platformDB(t)
	store := sites.NewPostgresStore(platform.DB())
	ctx := context.Background()
	if err := platform.CreateUser(ctx, &domain.User{ID: "deployer-u1", Username: "maria", Email: "maria@example.test",
		PasswordHash: "x", Role: "user", Active: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	seedSite(t, store, newSite("deployer-1", "proj-deployer", "deployer-shop"))
	now := time.Now().UTC().Truncate(time.Microsecond)
	empty := map[string]cfworkers.ManifestEntry{}
	finishDeploy(t, store, &sites.Deploy{ID: "deployer-d1", SiteID: "deployer-1", ProjectID: "proj-deployer",
		Manifest: empty, CreatedBy: "deployer-u1", CreatedAt: now}, sites.DeploySucceeded)
	finishDeploy(t, store, &sites.Deploy{ID: "deployer-d2", SiteID: "deployer-1", ProjectID: "proj-deployer",
		Manifest: empty, CreatedBy: "gone-user", CreatedAt: now.Add(time.Minute)}, sites.DeploySucceeded)

	listed, err := store.ListDeploys(ctx, "proj-deployer", "deployer-1", 10)
	if err != nil || len(listed) != 2 {
		t.Fatalf("listed = %v err=%v", listed, err)
	}
	if listed[1].CreatedByName != "maria" || listed[0].CreatedByName != "" {
		t.Fatalf("names = %q, %q; want maria for a known user and nothing for one that is gone",
			listed[1].CreatedByName, listed[0].CreatedByName)
	}
	read, err := store.GetDeploy(ctx, "proj-deployer", "deployer-1", "deployer-d1")
	if err != nil || read.CreatedByName != "maria" {
		t.Fatalf("read = %+v err=%v", read, err)
	}
}
