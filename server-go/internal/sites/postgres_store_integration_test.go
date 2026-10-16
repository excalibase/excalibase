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
	"github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
)

// Runs the Postgres store against a real platform database, which also proves migration 000076 applies.
func TestPostgresSiteStore(t *testing.T) {
	platform := pgstore.New(t)
	store := sites.NewPostgresStore(platform.DB())
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	newSite := func(id, project, name string) *sites.Site {
		return &sites.Site{ID: id, ProjectID: project, Name: name, Hostname: name + ".example.app",
			SPAFallback: true, CreatedBy: "dev", CreatedAt: now, UpdatedAt: now}
	}

	t.Run("create, name taken across projects, limit", func(t *testing.T) {
		if err := store.Create(ctx, newSite("s1", "proj-a", "shop"), 2); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := store.Create(ctx, newSite("s2", "proj-b", "shop"), 2); !errors.Is(err, sites.ErrSiteNameTaken) {
			t.Fatalf("err = %v, want name taken", err)
		}
		if err := store.Create(ctx, newSite("s3", "proj-a", "blog"), 2); err != nil {
			t.Fatalf("second create: %v", err)
		}
		if err := store.Create(ctx, newSite("s4", "proj-a", "docs"), 2); !errors.Is(err, sites.ErrSiteLimitReached) {
			t.Fatalf("err = %v, want the limit", err)
		}
	})

	t.Run("reads are scoped by project", func(t *testing.T) {
		site, err := store.Get(ctx, "proj-b", "s1")
		if err != nil || site != nil {
			t.Fatalf("another project read site=%v err=%v", site, err)
		}
		listed, err := store.List(ctx, "proj-a")
		if err != nil || len(listed) != 2 || listed[0].Name != "blog" {
			t.Fatalf("list = %v err=%v", listed, err)
		}
		if _, err := store.UpdateSettings(ctx, "proj-b", "s1", false); !errors.Is(err, sites.ErrSiteNotFound) {
			t.Fatalf("err = %v, want not found", err)
		}
	})

	t.Run("settings and cors claim", func(t *testing.T) {
		updated, err := store.UpdateSettings(ctx, "proj-a", "s1", false)
		if err != nil || updated.SPAFallback {
			t.Fatalf("updated = %+v err=%v", updated, err)
		}
		if err := store.MarkCorsClaimed(ctx, "proj-a", "s1"); err != nil {
			t.Fatalf("mark: %v", err)
		}
		site, _ := store.Get(ctx, "proj-a", "s1")
		if !site.CorsClaimed {
			t.Fatal("cors claim not stored")
		}
	})

	t.Run("lease", func(t *testing.T) {
		if err := store.ClaimDeploy(ctx, "proj-a", "s1", time.Now().Add(time.Minute)); err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := store.ClaimDeploy(ctx, "proj-a", "s1", time.Now().Add(time.Minute)); !errors.Is(err, sites.ErrSiteBusy) {
			t.Fatalf("err = %v, want busy", err)
		}
		if err := store.ClaimDeploy(ctx, "proj-b", "s1", time.Now().Add(time.Minute)); !errors.Is(err, sites.ErrSiteNotFound) {
			t.Fatalf("err = %v, want not found", err)
		}
		if err := store.ReleaseDeploy(ctx, "proj-a", "s1"); err != nil {
			t.Fatalf("release: %v", err)
		}
		if err := store.ClaimDeploy(ctx, "proj-a", "s1", time.Now().Add(-time.Second)); err != nil {
			t.Fatalf("claim after release: %v", err)
		}
		if err := store.ClaimDeploy(ctx, "proj-a", "s1", time.Now().Add(time.Minute)); err != nil {
			t.Fatalf("an expired lease must not hold the site: %v", err)
		}
		if err := store.ReleaseDeploy(ctx, "proj-a", "s1"); err != nil {
			t.Fatalf("release: %v", err)
		}
	})

	t.Run("deploys", func(t *testing.T) {
		deploy := &sites.Deploy{ID: "d1", SiteID: "s1", ProjectID: "proj-a", Status: sites.DeployPending,
			Manifest:  map[string]cfworkers.ManifestEntry{"/index.html": {Hash: "0123456789abcdef0123456789abcdef", Size: 12}},
			FileCount: 1, TotalBytes: 12, SPAFallback: true, Label: "main@abc", CreatedBy: "dev", CreatedAt: now}
		if err := store.CreateDeploy(ctx, deploy); err != nil {
			t.Fatalf("create deploy: %v", err)
		}
		finished := now.Add(time.Second)
		deploy.Status, deploy.FinishedAt = sites.DeploySucceeded, &finished
		if err := store.FinishDeploy(ctx, deploy); err != nil {
			t.Fatalf("finish: %v", err)
		}
		site, _ := store.Get(ctx, "proj-a", "s1")
		if site.LiveDeployID != "d1" {
			t.Fatalf("live = %q", site.LiveDeployID)
		}
		read, err := store.GetDeploy(ctx, "proj-a", "s1", "d1")
		if err != nil || read.Manifest["/index.html"].Size != 12 || read.Label != "main@abc" || read.FinishedAt == nil {
			t.Fatalf("read = %+v err=%v", read, err)
		}
		if other, _ := store.GetDeploy(ctx, "proj-b", "s1", "d1"); other != nil {
			t.Fatal("another project read the deploy")
		}
		failed := &sites.Deploy{ID: "d2", SiteID: "s1", ProjectID: "proj-a", Status: sites.DeployPending,
			Manifest: map[string]cfworkers.ManifestEntry{}, CreatedBy: "dev", CreatedAt: now.Add(time.Minute)}
		if err := store.CreateDeploy(ctx, failed); err != nil {
			t.Fatalf("create: %v", err)
		}
		failed.Status, failed.FailureReason, failed.FinishedAt = sites.DeployFailed, "cdn down", &finished
		if err := store.FinishDeploy(ctx, failed); err != nil {
			t.Fatalf("finish failed: %v", err)
		}
		site, _ = store.Get(ctx, "proj-a", "s1")
		if site.LiveDeployID != "d1" {
			t.Fatalf("a failed deploy moved live to %q", site.LiveDeployID)
		}
		listed, err := store.ListDeploys(ctx, "proj-a", "s1", 10)
		if err != nil || len(listed) != 2 || listed[0].ID != "d2" {
			t.Fatalf("listed = %v err=%v", listed, err)
		}
		if err := store.SetLive(ctx, "proj-b", "s1", "d1"); !errors.Is(err, sites.ErrSiteNotFound) {
			t.Fatalf("err = %v, want not found", err)
		}
	})

	t.Run("delete removes the history", func(t *testing.T) {
		if err := store.Delete(ctx, "proj-b", "s1"); !errors.Is(err, sites.ErrSiteNotFound) {
			t.Fatalf("err = %v, want not found", err)
		}
		if err := store.Delete(ctx, "proj-a", "s1"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if read, _ := store.GetDeploy(ctx, "proj-a", "s1", "d1"); read != nil {
			t.Fatal("deploys outlived their site")
		}
		if err := store.ReleaseDeploy(ctx, "proj-a", "s1"); err != nil {
			t.Fatalf("release of a deleted site: %v", err)
		}
	})
}

// The whole service against Postgres with a fake CDN: deploy, roll back, delete.
func TestServiceAgainstPostgres(t *testing.T) {
	platform := pgstore.New(t)
	store := sites.NewPostgresStore(platform.DB())
	service, err := sites.NewService(store, &acceptingPublisher{}, platform, sites.Options{BaseDomain: "example.app", SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	site, err := service.Create(ctx, "proj-int", "storefront", nil, "dev")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := service.Deploy(ctx, sites.DeployRequest{ProjectID: "proj-int", SiteID: site.ID, Actor: "dev", Body: bytes.NewReader(sampleArchive(t))})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	origins, err := platform.GetCorsOrigins(ctx, "proj-int")
	if err != nil || len(origins) != 1 || origins[0] != "https://storefront.example.app" {
		t.Fatalf("origins = %v err=%v", origins, err)
	}
	if _, err := service.Deploy(ctx, sites.DeployRequest{ProjectID: "proj-int", SiteID: site.ID, Actor: "dev", Body: bytes.NewReader(sampleArchive(t))}); err != nil {
		t.Fatalf("second deploy: %v", err)
	}
	rolled, err := service.Rollback(ctx, "proj-int", site.ID, first.ID)
	if err != nil || rolled.LiveDeployID != first.ID {
		t.Fatalf("rollback = %+v err=%v", rolled, err)
	}
	if err := service.Delete(ctx, "proj-int", site.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	origins, _ = platform.GetCorsOrigins(ctx, "proj-int")
	if len(origins) != 0 {
		t.Fatalf("origins after delete = %v", origins)
	}
}
