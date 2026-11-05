//go:build integration

package sitebuilds_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

// A server site gets no preview from git (EXC-660): its preview would publish its server code as public files.

func TestServerSitePreviewModeIsRefused(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{ServerSite: true}, sitebuilds.PreviewModeOff)
	_, err := h.Service.SetPreviewMode(context.Background(), h.Project, h.Site.ID, sitebuilds.PreviewModeBranches)
	if !errors.Is(err, sites.ErrPreviewOfServerSite) {
		t.Fatalf("err = %v, want previews refused for a server site", err)
	}
	if _, err := h.Service.SetPreviewMode(context.Background(), h.Project, h.Site.ID, sitebuilds.PreviewModeOff); err != nil {
		t.Fatalf("turning previews off on a server site: %v", err)
	}
}

func TestServerSitePushesBuildProductionButNoPreview(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{ServerSite: true}, sitebuilds.PreviewModeOff)
	ctx := context.Background()
	// Previews switched on before server sites were refused: the stored mode must still make none.
	if err := h.Store.SavePreviewMode(ctx, h.Project, h.Site.ID, sitebuilds.PreviewModeBranches); err != nil {
		t.Fatal(err)
	}
	if queued, err := h.Push("feat/header", featSHA, "d-ssr-1"); err != nil || queued != 0 {
		t.Fatalf("a branch push to a server site queued %d, %v; want nothing and no error", queued, err)
	}
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(9, true, prSHA, h.SiteRepo))
	if queued, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 9, prSHA, h.SiteRepo, "d-ssr-2"); err != nil || queued != 0 {
		t.Fatalf("a pull request on a server site queued %d, %v", queued, err)
	}
	if got := previews(t, h); len(got) != 0 {
		t.Fatalf("a server site holds previews %+v", got)
	}
	for host, script := range h.Publisher.Hosts {
		if strings.Contains(host, "--") || strings.HasPrefix(script, "site-pv-") {
			t.Fatalf("a preview host is routed: %s -> %s", host, script)
		}
	}
	before, _ := h.Sites.Get(ctx, h.Project, h.Site.ID)
	if queued, err := h.Push("main", prNewSHA, "d-ssr-3"); err != nil || queued != 1 {
		t.Fatalf("the production push queued %d, %v", queued, err)
	}
	h.RunAll()
	build := h.Builds()[0]
	after, _ := h.Sites.Get(ctx, h.Project, h.Site.ID)
	if build.Status != sitebuilds.StatusSucceeded || build.PreviewID != "" || after.LiveDeployID == before.LiveDeployID {
		t.Fatalf("production build = %+v, live %s -> %s", build, before.LiveDeployID, after.LiveDeployID)
	}
}

func TestRedeliveredPushLeavesTheReadyPreviewReady(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	if _, err := h.Push("feat/header", featSHA, "d-again-1"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	if preview := onlyPreview(t, h); preview.Status != sites.PreviewReady {
		t.Fatalf("preview = %+v", preview)
	}
	h.ForgetDelivery("d-again-1")
	if queued, err := h.Push("feat/header", featSHA, "d-again-1"); err != nil || queued != 0 {
		t.Fatalf("redelivery queued %d, %v", queued, err)
	}
	if preview := onlyPreview(t, h); preview.Status != sites.PreviewReady || preview.DeployedSHA != featSHA {
		t.Fatalf("after a redelivery that built nothing the preview is %+v; want it still READY", preview)
	}
}

func TestRedeliveredPullRequestLeavesTheReadyPreviewReady(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModePullRequests)
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(21, true, prSHA, h.SiteRepo))
	if _, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 21, prSHA, h.SiteRepo, "d-again-pr"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	h.ForgetDelivery("d-again-pr")
	if queued, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 21, prSHA, h.SiteRepo, "d-again-pr"); err != nil || queued != 0 {
		t.Fatalf("redelivery queued %d, %v", queued, err)
	}
	if preview := onlyPreview(t, h); preview.Status != sites.PreviewReady {
		t.Fatalf("after a redelivery that built nothing the preview is %+v; want it still READY", preview)
	}
}
