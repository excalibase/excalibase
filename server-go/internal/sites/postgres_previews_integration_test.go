//go:build integration

package sites_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

// Runs the preview store against a real platform database, which also proves the previews migration applies.

func newPreview(id, site, project, ref string) *sites.Preview {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &sites.Preview{ID: id, SiteID: site, ProjectID: project, Source: sites.PreviewSourceBranch, Ref: ref,
		Hostname: id + "--host.excalibase.app", Status: sites.PreviewBuilding, CommitSHA: "c1", CreatedBy: "dev", CreatedAt: now, UpdatedAt: now}
}

func TestPostgresPreviewOpenIsStablePerRefAndCapped(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("pv-cap-a", "proj-pv-cap", "pv-cap-a"))
	first, err := store.OpenPreview(ctx, newPreview("pv-cap-1", "pv-cap-a", "proj-pv-cap", "feat/a"), 1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	again := newPreview("pv-cap-2", "pv-cap-a", "proj-pv-cap", "feat/a")
	again.CommitSHA = "c2"
	reopened, err := store.OpenPreview(ctx, again, 1, 1000)
	if err != nil || reopened.ID != first.ID || reopened.Hostname != first.Hostname || reopened.CommitSHA != "c2" {
		t.Fatalf("reopen = %+v, %v; want the same preview at the new commit", reopened, err)
	}
	if _, err := store.OpenPreview(ctx, newPreview("pv-cap-3", "pv-cap-a", "proj-pv-cap", "feat/b"), 1, 1000); !errors.Is(err, sites.ErrPreviewLimitReached) {
		t.Fatalf("err = %v, want ErrPreviewLimitReached", err)
	}
	count, err := store.CountPreviews(ctx, "proj-pv-cap")
	if err != nil || count != 1 {
		t.Fatalf("count = %d, %v", count, err)
	}
}

func TestPostgresPreviewInstallCapHoldsUnderConcurrentOpens(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	var total int
	if err := platformDB(t).DB().QueryRow(`SELECT count(*) FROM site_previews`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	seedSite(t, store, newSite("pv-race-a", "proj-pv-race", "pv-race-a"))
	var wait sync.WaitGroup
	results := make([]error, 6)
	for i := range results {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, results[i] = store.OpenPreview(ctx, newPreview("pv-race-"+string(rune('a'+i)), "pv-race-a", "proj-pv-race", "b"+string(rune('a'+i))), 100, total+2)
		}(i)
	}
	wait.Wait()
	opened := 0
	for _, err := range results {
		if err == nil {
			opened++
		} else if !errors.Is(err, sites.ErrPreviewInstallFull) {
			t.Fatalf("unexpected err %v", err)
		}
	}
	if opened != 2 {
		t.Fatalf("opened %d previews under an install cap leaving room for 2", opened)
	}
}

func TestPostgresPreviewLeaseFinishRemoveAndDue(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("pv-life-a", "proj-pv-life", "pv-life-a"))
	preview, err := store.OpenPreview(ctx, newPreview("pv-life-1", "pv-life-a", "proj-pv-life", "feat/a"), 5, 1000)
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Minute)
	if err := store.ClaimPreview(ctx, "proj-pv-life", preview.ID, "lease-1", until, false); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimPreview(ctx, "proj-pv-life", preview.ID, "lease-2", until, false); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("second claim err = %v", err)
	}
	if err := store.ClaimPreview(ctx, "other-project", preview.ID, "lease-3", until, false); !errors.Is(err, sites.ErrPreviewNotFound) {
		t.Fatalf("another project's claim err = %v", err)
	}
	deployed := time.Now().UTC().Truncate(time.Microsecond)
	preview.Status, preview.DeployedSHA, preview.FileCount, preview.TotalBytes, preview.CorsClaimed, preview.DeployedAt = sites.PreviewReady, "c1", 3, 99, true, &deployed
	if err := store.FinishPreview(ctx, preview, "lease-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishPreview(ctx, preview, "lease-x"); !errors.Is(err, sites.ErrLeaseLost) {
		t.Fatalf("finish under a stale lease err = %v", err)
	}
	stored, err := store.GetPreview(ctx, "proj-pv-life", "pv-life-a", preview.ID)
	if err != nil || stored.Status != sites.PreviewReady || stored.DeployedSHA != "c1" || stored.FileCount != 3 || !stored.CorsClaimed || stored.DeployedAt == nil {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	if err := store.ReleasePreview(ctx, "proj-pv-life", preview.ID, "lease-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPreviewRemoving(ctx, "proj-pv-life", preview.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenPreview(ctx, newPreview("pv-life-2", "pv-life-a", "proj-pv-life", "feat/a"), 5, 1000); !errors.Is(err, sites.ErrPreviewRemoving) {
		t.Fatalf("reopen while removing err = %v", err)
	}
	if err := store.ClaimPreview(ctx, "proj-pv-life", preview.ID, "lease-4", until, false); !errors.Is(err, sites.ErrPreviewNotFound) {
		t.Fatalf("a deploy claim on a removing preview err = %v", err)
	}
	due, err := store.DuePreviews(ctx, time.Now().Add(-time.Hour), 100)
	if err != nil || !containsPreview(due, preview.ID) {
		t.Fatalf("due = %v, %v; want the removing preview", due, err)
	}
	if err := store.ClaimPreview(ctx, "proj-pv-life", preview.ID, "lease-5", until, true); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePreview(ctx, "proj-pv-life", preview.ID, "lease-5"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.FindPreview(ctx, "proj-pv-life", "pv-life-a", sites.PreviewSourceBranch, "feat/a"); got != nil {
		t.Fatalf("found after delete: %+v", got)
	}
}

func containsPreview(previews []*sites.Preview, id string) bool {
	for _, preview := range previews {
		if preview.ID == id {
			return true
		}
	}
	return false
}

func TestPostgresPreviewServiceRoundTripAndSiteDeletion(t *testing.T) {
	db := platformDB(t)
	store := sites.NewPostgresStore(db.DB())
	publisher := sitestest.NewPublisher()
	service, err := sites.NewService(store, publisher, sitestest.NewOrigins(), sites.Options{BaseDomain: "excalibase.app", SpoolDir: t.TempDir(),
		Previews: store})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	site, err := service.Create(ctx, "proj-pv-svc", "pv-svc-site", nil, "dev")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.OpenPreview(ctx, sites.PreviewRequest{ProjectID: "proj-pv-svc", SiteID: site.ID, Source: sites.PreviewSourcePullRequest, Ref: "4", CommitSHA: "c4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeployPreview(ctx, sites.PreviewDeployRequest{ProjectID: "proj-pv-svc", SiteID: site.ID, PreviewID: preview.ID,
		CommitSHA: "c4", Body: bytes.NewReader(sampleArchive(t))}); err != nil {
		t.Fatal(err)
	}
	if publisher.Hosts[preview.Hostname] != sites.PreviewScriptName(preview) {
		t.Fatalf("hosts = %v", publisher.Hosts)
	}
	if err := service.Delete(ctx, "proj-pv-svc", site.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.DB().QueryRow(`SELECT count(*) FROM site_previews WHERE site_id = $1`, site.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("previews left = %d, %v", left, err)
	}
	if len(publisher.Hosts) != 0 {
		t.Fatalf("hosts left = %v", publisher.Hosts)
	}
}

// The sweep marks only a preview still idle: a push between its read and its mark spares the preview.
func TestPostgresPreviewIdleMarkSparesAFreshPreview(t *testing.T) {
	store := sites.NewPostgresStore(platformDB(t).DB())
	ctx := context.Background()
	seedSite(t, store, newSite("pv-idle-a", "proj-pv-idle", "pv-idle-a"))
	fork := newPreview("pv-idle-1", "pv-idle-a", "proj-pv-idle", "12")
	fork.Source, fork.FromFork = sites.PreviewSourcePullRequest, true
	preview, err := store.OpenPreview(ctx, fork, 5, 1000)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetPreview(ctx, "proj-pv-idle", "pv-idle-a", preview.ID)
	if err != nil || !stored.FromFork {
		t.Fatalf("stored = %+v, %v; want the fork remembered", stored, err)
	}
	if err := store.MarkPreviewRemoving(ctx, "proj-pv-idle", preview.ID, preview.UpdatedAt.Add(-time.Hour)); !errors.Is(err, sites.ErrPreviewNotFound) {
		t.Fatalf("idle mark of a fresh preview err = %v", err)
	}
	if again, _ := store.GetPreview(ctx, "proj-pv-idle", "pv-idle-a", preview.ID); again.Status == sites.PreviewRemoving {
		t.Fatal("a fresh preview was marked by the idle sweep")
	}
	if err := store.MarkPreviewRemoving(ctx, "proj-pv-idle", preview.ID, preview.UpdatedAt.Add(time.Hour)); err != nil {
		t.Fatalf("idle mark of an idle preview: %v", err)
	}
	// Once REMOVING, the idle check passes regardless, so a failed removal is retried.
	if err := store.MarkPreviewRemoving(ctx, "proj-pv-idle", preview.ID, preview.UpdatedAt.Add(-time.Hour)); err != nil {
		t.Fatalf("retry mark of a removing preview: %v", err)
	}
	reopen := newPreview("pv-idle-2", "pv-idle-a", "proj-pv-idle", "12")
	reopen.Source = sites.PreviewSourcePullRequest
	if _, err := store.OpenPreview(ctx, reopen, 5, 1000); !errors.Is(err, sites.ErrPreviewRemoving) {
		t.Fatalf("reopen of a removing preview err = %v", err)
	}
}
