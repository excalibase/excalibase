//go:build integration

package sites_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// Runs the Postgres store against a real platform database, which also proves migration 000076 applies.

func newSite(id, project, name string) *sites.Site {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &sites.Site{ID: id, ProjectID: project, Name: name, Hostname: name + ".example.app",
		SPAFallback: true, CreatedBy: "dev", CreatedAt: now, UpdatedAt: now}
}

func seedSite(t *testing.T, store *sites.PostgresStore, site *sites.Site) {
	t.Helper()
	if err := store.Create(context.Background(), site, 5); err != nil {
		t.Fatalf("seed site %s: %v", site.Name, err)
	}
}

func TestPostgresSiteNameIsGlobalAndTheLimitHolds(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	if err := store.Create(ctx, newSite("limit-1", "proj-limit-a", "limit-shop"), 2); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Create(ctx, newSite("limit-2", "proj-limit-b", "limit-shop"), 2); !errors.Is(err, sites.ErrSiteNameTaken) {
		t.Fatalf("err = %v, want name taken", err)
	}
	if err := store.Create(ctx, newSite("limit-3", "proj-limit-a", "limit-blog"), 2); err != nil {
		t.Fatalf("second create: %v", err)
	}
	if err := store.Create(ctx, newSite("limit-4", "proj-limit-a", "limit-docs"), 2); !errors.Is(err, sites.ErrSiteLimitReached) {
		t.Fatalf("err = %v, want the limit", err)
	}
}

func TestPostgresSiteReadsAndWritesAreScopedByProject(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("scope-1", "proj-scope-a", "scope-shop"))
	seedSite(t, store, newSite("scope-2", "proj-scope-a", "scope-blog"))
	if site, err := store.Get(ctx, "proj-scope-b", "scope-1"); err != nil || site != nil {
		t.Fatalf("another project read site=%v err=%v", site, err)
	}
	listed, err := store.List(ctx, "proj-scope-a")
	if err != nil || len(listed) != 2 || listed[0].Name != "scope-blog" {
		t.Fatalf("list = %v err=%v", listed, err)
	}
	if _, err := store.UpdateSettings(ctx, "proj-scope-b", "scope-1", false); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	if err := store.SetLive(ctx, "proj-scope-b", "scope-1", "d1", "lease"); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	if err := store.Delete(ctx, "proj-scope-b", "scope-1", "lease"); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestPostgresSiteSettingsAndCorsClaim(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("settings-1", "proj-settings", "settings-shop"))
	updated, err := store.UpdateSettings(ctx, "proj-settings", "settings-1", false)
	if err != nil || updated.SPAFallback {
		t.Fatalf("updated = %+v err=%v", updated, err)
	}
	if err := store.MarkCorsClaimed(ctx, "proj-settings", "settings-1"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if site, _ := store.Get(ctx, "proj-settings", "settings-1"); !site.CorsClaimed {
		t.Fatal("cors claim not stored")
	}
}

func TestPostgresSiteLease(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("lease-1", "proj-lease", "lease-shop"))
	soon := func() time.Time { return time.Now().Add(time.Minute) }
	if err := store.ClaimDeploy(ctx, "proj-lease", "lease-1", "lease-a", soon()); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.ClaimDeploy(ctx, "proj-lease", "lease-1", "lease-b", soon()); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("err = %v, want busy", err)
	}
	if err := store.ClaimDeploy(ctx, "proj-other", "lease-1", "lease-b", soon()); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	if err := store.ReleaseDeploy(ctx, "proj-lease", "lease-1", "lease-b"); err != nil {
		t.Fatalf("release by another holder: %v", err)
	}
	if err := store.ClaimDeploy(ctx, "proj-lease", "lease-1", "lease-b", soon()); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("another holder's release freed the lease: %v", err)
	}
	if err := store.ReleaseDeploy(ctx, "proj-lease", "lease-1", "lease-a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := store.ClaimDeploy(ctx, "proj-lease", "lease-1", "lease-c", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	if err := store.ClaimDeploy(ctx, "proj-lease", "lease-1", "lease-d", soon()); err != nil {
		t.Fatalf("an expired lease must not hold the site: %v", err)
	}
}

// A deploy whose lease expired and was taken over may not move the live pointer or delete the site.
func TestPostgresSiteStaleLeaseCannotWrite(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("stale-1", "proj-stale", "stale-shop"))
	if err := store.ClaimDeploy(ctx, "proj-stale", "stale-1", "lease-old", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.ClaimDeploy(ctx, "proj-stale", "stale-1", "lease-new", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("take over: %v", err)
	}
	deploy := &sites.Deploy{ID: "stale-d1", SiteID: "stale-1", ProjectID: "proj-stale", Status: sites.DeployPending,
		Manifest: map[string]cfworkers.ManifestEntry{}, CreatedBy: "dev", CreatedAt: time.Now()}
	if err := store.CreateDeploy(ctx, deploy); err != nil {
		t.Fatalf("create deploy: %v", err)
	}
	finished := time.Now()
	deploy.Status, deploy.FinishedAt = sites.DeploySucceeded, &finished
	if err := store.FinishDeploy(ctx, deploy, "lease-old"); !errors.Is(err, sites.ErrLeaseLost) {
		t.Fatalf("finish with a lost lease: %v", err)
	}
	if err := store.SetLive(ctx, "proj-stale", "stale-1", "stale-d1", "lease-old"); !errors.Is(err, sites.ErrLeaseLost) {
		t.Fatalf("set live with a lost lease: %v", err)
	}
	if err := store.Delete(ctx, "proj-stale", "stale-1", "lease-old"); !errors.Is(err, sites.ErrLeaseLost) {
		t.Fatalf("delete with a lost lease: %v", err)
	}
	if site, _ := store.Get(ctx, "proj-stale", "stale-1"); site == nil || site.LiveDeployID != "" {
		t.Fatalf("site = %+v, want it kept and not live", site)
	}
	if err := store.FinishDeploy(ctx, deploy, "lease-new"); err != nil {
		t.Fatalf("finish with the current lease: %v", err)
	}
}

func TestPostgresSiteDeploysAndLivePointer(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("deploys-1", "proj-deploys", "deploys-shop"))
	now := time.Now().UTC().Truncate(time.Microsecond)
	finishDeploy(t, store, &sites.Deploy{ID: "deploys-d1", SiteID: "deploys-1", ProjectID: "proj-deploys",
		Manifest:  map[string]cfworkers.ManifestEntry{"/index.html": {Hash: "0123456789abcdef0123456789abcdef", Size: 12}},
		FileCount: 1, TotalBytes: 12, SPAFallback: true, Label: "main@abc", CreatedBy: "dev", CreatedAt: now}, sites.DeploySucceeded)
	finishDeploy(t, store, &sites.Deploy{ID: "deploys-d2", SiteID: "deploys-1", ProjectID: "proj-deploys",
		Manifest: map[string]cfworkers.ManifestEntry{}, CreatedBy: "dev", CreatedAt: now.Add(time.Minute)}, sites.DeployFailed)

	if site, _ := store.Get(ctx, "proj-deploys", "deploys-1"); site.LiveDeployID != "deploys-d1" {
		t.Fatalf("live = %q, want the deploy that succeeded", site.LiveDeployID)
	}
	read, err := store.GetDeploy(ctx, "proj-deploys", "deploys-1", "deploys-d1")
	if err != nil || read.Manifest["/index.html"].Size != 12 || read.Label != "main@abc" || read.FinishedAt == nil {
		t.Fatalf("read = %+v err=%v", read, err)
	}
	if other, _ := store.GetDeploy(ctx, "proj-other", "deploys-1", "deploys-d1"); other != nil {
		t.Fatal("another project read the deploy")
	}
	listed, err := store.ListDeploys(ctx, "proj-deploys", "deploys-1", 10)
	if err != nil || len(listed) != 2 || listed[0].ID != "deploys-d2" {
		t.Fatalf("listed = %v err=%v", listed, err)
	}
}

func TestPostgresSiteDeployForAnotherProjectIsRefused(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	seedSite(t, store, newSite("foreign-1", "proj-foreign", "foreign-shop"))
	deploy := &sites.Deploy{ID: "foreign-d1", SiteID: "foreign-1", ProjectID: "proj-intruder", Status: sites.DeployPending,
		Manifest: map[string]cfworkers.ManifestEntry{}, CreatedBy: "dev", CreatedAt: time.Now()}
	if err := store.CreateDeploy(context.Background(), deploy); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestPostgresSiteDeleteRemovesTheHistory(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("delete-1", "proj-delete", "delete-shop"))
	finishDeploy(t, store, &sites.Deploy{ID: "delete-d1", SiteID: "delete-1", ProjectID: "proj-delete",
		Manifest: map[string]cfworkers.ManifestEntry{}, CreatedBy: "dev", CreatedAt: time.Now()}, sites.DeploySucceeded)
	if err := store.ClaimDeploy(ctx, "proj-delete", "delete-1", "lease-del", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.Delete(ctx, "proj-delete", "delete-1", "lease-del"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if read, _ := store.GetDeploy(ctx, "proj-delete", "delete-1", "delete-d1"); read != nil {
		t.Fatal("deploys outlived their site")
	}
	if err := store.ReleaseDeploy(ctx, "proj-delete", "delete-1", "lease-del"); err != nil {
		t.Fatalf("release of a deleted site: %v", err)
	}
}

func finishDeploy(t *testing.T, store *sites.PostgresStore, deploy *sites.Deploy, status string) {
	t.Helper()
	deploy.Status = sites.DeployPending
	if err := store.CreateDeploy(context.Background(), deploy); err != nil {
		t.Fatalf("create deploy %s: %v", deploy.ID, err)
	}
	finished := deploy.CreatedAt.Add(time.Second)
	deploy.Status, deploy.FinishedAt = status, &finished
	if status == sites.DeployFailed {
		deploy.FailureReason = "cdn down"
	}
	lease := "lease-" + deploy.ID
	if err := store.ClaimDeploy(context.Background(), deploy.ProjectID, deploy.SiteID, lease, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("claim for %s: %v", deploy.ID, err)
	}
	if err := store.FinishDeploy(context.Background(), deploy, lease); err != nil {
		t.Fatalf("finish deploy %s: %v", deploy.ID, err)
	}
	if err := store.ReleaseDeploy(context.Background(), deploy.ProjectID, deploy.SiteID, lease); err != nil {
		t.Fatalf("release for %s: %v", deploy.ID, err)
	}
}

// The whole service against Postgres with a fake CDN: deploy, roll back, delete.
func TestServiceAgainstPostgres(t *testing.T) {
	platform := platformDB(t)
	service, err := sites.NewService(sites.NewPostgresStore(platform.DB()), &acceptingPublisher{}, platform,
		sites.Options{BaseDomain: "example.app", SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	site, err := service.Create(ctx, "proj-int", "storefront", nil, "dev")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	deployOnce := func() *sites.Deploy {
		deploy, err := service.Deploy(ctx, sites.DeployRequest{ProjectID: "proj-int", SiteID: site.ID, Actor: "dev", Body: bytes.NewReader(sampleArchive(t))})
		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		return deploy
	}
	first := deployOnce()
	if origins, err := platform.GetCorsOrigins(ctx, "proj-int"); err != nil || len(origins) != 1 || origins[0] != "https://storefront.example.app" {
		t.Fatalf("origins = %v err=%v", origins, err)
	}
	deployOnce()
	if rolled, err := service.Rollback(ctx, "proj-int", site.ID, first.ID); err != nil || rolled.LiveDeployID != first.ID {
		t.Fatalf("rollback = %+v err=%v", rolled, err)
	}
	if err := service.Delete(ctx, "proj-int", site.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if origins, _ := platform.GetCorsOrigins(ctx, "proj-int"); len(origins) != 0 {
		t.Fatalf("origins after delete = %v", origins)
	}
}
