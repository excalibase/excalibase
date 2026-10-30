package sites_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

// darkTwin is a service over the same stores with previews dark, as after the flag is turned off.
func (h *previewHarness) darkTwin(t *testing.T) *sites.Service {
	t.Helper()
	service, err := sites.NewService(h.store, h.publisher, h.origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(),
		DeployLimits: previewLimits{max: 5}, Previews: h.previews, PreviewsDark: true})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.SetNow(func() time.Time { return h.now })
	return service
}

// Turning the flag off must not orphan what previews already put on Cloudflare.
func TestDarkPreviewsRefuseNewOnesButStillCleanUp(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	if _, err := h.deployPreview(t, preview, false); err != nil {
		t.Fatal(err)
	}
	dark := h.darkTwin(t)
	if dark.PreviewsAvailable() || !dark.KeepsPreviews() {
		t.Fatalf("available=%v keeps=%v; want new previews refused and old ones kept for cleanup", dark.PreviewsAvailable(), dark.KeepsPreviews())
	}
	if _, err := dark.OpenPreview(context.Background(), sites.PreviewRequest{ProjectID: testProject, SiteID: site.ID,
		Source: sites.PreviewSourceBranch, Ref: "feat/b", CommitSHA: "c"}); !errors.Is(err, sites.ErrPreviewsUnavailable) {
		t.Fatalf("open while dark: err = %v", err)
	}
	if err := dark.Delete(context.Background(), testProject, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.publisher.Published[sites.PreviewScriptName(preview)]; ok || len(h.publisher.Hosts) != 0 {
		t.Fatalf("the preview outlived its site with previews dark: hosts %v", h.publisher.Hosts)
	}
}

func TestDarkPreviewsAreStillSwept(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	h.mustOpen(t, site, sites.PreviewSourceBranch, "old")
	h.now = h.now.Add(sites.PreviewLifetime + time.Hour)
	if report := h.darkTwin(t).SweepPreviews(context.Background()); report.Removed != 1 {
		t.Fatalf("report = %+v", report)
	}
}

// One preview that cannot be removed must not leave the others served and unmarked.
func TestRemovingASitesPreviewsMarksEveryOneEvenWhenOneFails(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	h.mustOpen(t, site, sites.PreviewSourceBranch, "a")
	h.mustOpen(t, site, sites.PreviewSourceBranch, "b")
	h.publisher.FailKV = errors.New("kv down")
	if err := h.service.Delete(context.Background(), testProject, site.ID); err == nil {
		t.Fatal("want the deletion to fail")
	}
	held, _ := h.previews.ListPreviews(context.Background(), testProject, site.ID)
	for _, preview := range held {
		if preview.Status != sites.PreviewRemoving {
			t.Fatalf("preview %s is %s; every preview should be left for the sweep", preview.Ref, preview.Status)
		}
	}
}

// pushedMeanwhile is a store where a push lands between the sweep's read and its mark.
type pushedMeanwhile struct {
	*sitestest.MemoryPreviews
	at time.Time
}

func (p pushedMeanwhile) DuePreviews(ctx context.Context, idleBefore time.Time, limit int) ([]*sites.Preview, error) {
	due, err := p.MemoryPreviews.DuePreviews(ctx, idleBefore, limit)
	for _, preview := range due {
		held := p.Previews[preview.ID]
		held.UpdatedAt = p.at
		p.Previews[preview.ID] = held
	}
	return due, err
}

func TestSweepSparesAPreviewPushedSinceItWasListed(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourceBranch, "busy")
	h.now = h.now.Add(sites.PreviewLifetime + time.Hour)
	store := pushedMeanwhile{MemoryPreviews: h.previews, at: h.now}
	service, err := sites.NewService(h.store, h.publisher, h.origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(),
		DeployLimits: previewLimits{max: 5}, Previews: store})
	if err != nil {
		t.Fatal(err)
	}
	service.SetNow(func() time.Time { return h.now })
	if report := service.SweepPreviews(context.Background()); report.Removed != 0 {
		t.Fatalf("report = %+v; the preview was pushed to after the sweep listed it", report)
	}
	if got, _ := h.previews.GetPreview(context.Background(), testProject, site.ID, preview.ID); got == nil || got.Status == sites.PreviewRemoving {
		t.Fatalf("stored = %+v", got)
	}
}

// A project scheduled for deletion stops serving its previews too, and gets them back on a cancel.
func TestUnroutingAProjectUnroutesItsPreviewsAndRoutingRestoresThem(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	if _, err := h.deploy(t, site, sitestest.Dist(t, "<h1>live</h1>")); err != nil {
		t.Fatal(err)
	}
	ready := h.mustOpen(t, site, sites.PreviewSourceBranch, "ready")
	if _, err := h.deployPreview(t, ready, false); err != nil {
		t.Fatal(err)
	}
	building := h.mustOpen(t, site, sites.PreviewSourceBranch, "building")
	if err := h.service.UnrouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.publisher.Hosts[ready.Hostname]; ok {
		t.Fatal("the preview stayed routed while its project waits for deletion")
	}
	if err := h.service.RouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatal(err)
	}
	if h.publisher.Hosts[ready.Hostname] != sites.PreviewScriptName(ready) {
		t.Fatalf("hosts = %v; the deployed preview should be served again", h.publisher.Hosts)
	}
	if _, ok := h.publisher.Hosts[building.Hostname]; ok {
		t.Fatal("a preview never deployed was routed")
	}
}

// An origin added while API access was on goes once a deploy runs without it.
func TestDeployWithoutAPIAccessReleasesTheOriginItHeld(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourcePullRequest, "3")
	if _, err := h.deployPreview(t, preview, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deployPreview(t, preview, false); err != nil {
		t.Fatal(err)
	}
	if len(h.origins.Origins) != 0 {
		t.Fatalf("origins = %v; API access was withdrawn", h.origins.Origins)
	}
	if _, err := h.deployPreview(t, preview, true); err != nil {
		t.Fatal(err)
	}
	if len(h.origins.Origins) != 1 {
		t.Fatalf("origins = %v; API access was granted again", h.origins.Origins)
	}
}

func TestWithdrawingAPIAccessReleasesEveryPreviewOriginNow(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	for _, ref := range []string{"1", "2"} {
		preview := h.mustOpen(t, site, sites.PreviewSourcePullRequest, ref)
		if _, err := h.deployPreview(t, preview, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.service.WithdrawPreviewAPIAccess(context.Background(), testProject, site.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.origins.Origins) != 0 {
		t.Fatalf("origins = %v", h.origins.Origins)
	}
	held, _ := h.previews.ListPreviews(context.Background(), testProject, site.ID)
	for _, preview := range held {
		if preview.Status != sites.PreviewReady {
			t.Fatalf("preview %s is %s; withdrawing access keeps it served", preview.Ref, preview.Status)
		}
	}
}

func TestPreviewRemembersAForkPullRequest(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview, err := h.service.OpenPreview(context.Background(), sites.PreviewRequest{ProjectID: testProject, SiteID: site.ID,
		Source: sites.PreviewSourcePullRequest, Ref: "9", CommitSHA: "c", FromFork: true})
	if err != nil || !preview.FromFork {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
}

// Previews draw on their own hourly budget, so pull requests cannot starve the production branch.
func TestPreviewBuildsHaveTheirOwnHourlyBudget(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	ctx := context.Background()
	for range sites.DefaultLimits.MaxDeploysPerHour {
		if err := h.service.TakePreviewSlot(ctx, testProject); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.service.TakePreviewSlot(ctx, testProject); !errors.Is(err, sites.ErrDeployRateLimited) {
		t.Fatalf("err = %v, want the preview budget spent", err)
	}
	if err := h.service.TakeDeploySlot(ctx, testProject); err != nil {
		t.Fatalf("the production budget was spent by previews: %v", err)
	}
}
