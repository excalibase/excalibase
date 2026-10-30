//go:build integration

package sitebuilds_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

const forkRepo = int64(987654)

// Turning the flag off stops new previews, yet closing a pull request still takes its preview down.
func TestDarkPreviewsBuildNothingNewButClosingAPullRequestStillRemovesIt(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModePullRequests)
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(5, true, prSHA, h.SiteRepo))
	if _, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 5, prSHA, h.SiteRepo, "d-dark-1"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	h.TurnPreviewsDark()
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(5, true, prNewSHA, h.SiteRepo))
	if queued, err := h.DeliverPullRequest(gitprovider.PullRequestSynchronize, 5, prNewSHA, h.SiteRepo, "d-dark-2"); err != nil || queued != 0 {
		t.Fatalf("a dark install queued %d preview builds, %v", queued, err)
	}
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(5, false, prNewSHA, h.SiteRepo))
	if _, err := h.DeliverPullRequest(gitprovider.PullRequestClosed, 5, prNewSHA, h.SiteRepo, "d-dark-3"); err != nil {
		t.Fatal(err)
	}
	if len(h.Publisher.Hosts) != 1 {
		t.Fatalf("hosts = %v; the closed pull request's preview outlived the flag", h.Publisher.Hosts)
	}
}

func TestSwitchingModeRemovesPreviewsTheNewModeDoesNotMake(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	if _, err := h.Push("feat/a", featSHA, "d-mode-1"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	if _, err := h.Service.SetPreviewMode(context.Background(), h.Project, h.Site.ID, sitebuilds.PreviewModePullRequests); err != nil {
		t.Fatal(err)
	}
	if got := previews(t, h); len(got) != 0 {
		t.Fatalf("previews = %+v; a branch preview outlived the switch to pull requests", got)
	}
}

func TestTurningForksOffRemovesForkPreviewsOnly(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModePullRequests)
	ctx := context.Background()
	if _, err := h.Service.SetPreviewAccess(ctx, h.Project, h.Site.ID, sitebuilds.PreviewAccess{Forks: true}); err != nil {
		t.Fatal(err)
	}
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(31, true, prSHA, forkRepo))
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(32, true, prNewSHA, h.SiteRepo))
	if _, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 31, prSHA, forkRepo, "d-forks-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 32, prNewSHA, h.SiteRepo, "d-forks-2"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	if _, err := h.Service.SetPreviewAccess(ctx, h.Project, h.Site.ID, sitebuilds.PreviewAccess{}); err != nil {
		t.Fatal(err)
	}
	if left := onlyPreview(t, h); left.Ref != "32" {
		t.Fatalf("left = %+v; want only the same-repository pull request", left)
	}
}

func TestWithdrawingAPIAccessTakesPreviewOriginsOffTheCORSList(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	ctx := context.Background()
	if _, err := h.Service.SetPreviewAccess(ctx, h.Project, h.Site.ID, sitebuilds.PreviewAccess{APIAccess: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Push("feat/a", featSHA, "d-api-1"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	preview := onlyPreview(t, h)
	if !slices.Contains(h.Origins.Origins, preview.URL()) {
		t.Fatalf("origins = %v; want the preview's", h.Origins.Origins)
	}
	if _, err := h.Service.SetPreviewAccess(ctx, h.Project, h.Site.ID, sitebuilds.PreviewAccess{}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(h.Origins.Origins, preview.URL()) {
		t.Fatalf("origins = %v after API access was turned off", h.Origins.Origins)
	}
}

// A build queued before the Admin withdrew secrets runs without them.
func TestSecretsWithdrawnWhileABuildWaitsAreNotGiven(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{Secrets: map[string]string{"STRIPE_PK": "pk_live_secret"}}, sitebuilds.PreviewModeBranches)
	ctx := context.Background()
	if _, err := h.Service.SetSecretVisibility(ctx, h.Project, []string{"STRIPE_PK"}, h.User); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Service.SetPreviewAccess(ctx, h.Project, h.Site.ID, sitebuilds.PreviewAccess{Secrets: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Push("feat/a", featSHA, "d-late-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Service.SetPreviewAccess(ctx, h.Project, h.Site.ID, sitebuilds.PreviewAccess{}); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	specs := h.Runner.Specs()
	if _, leaked := specs[len(specs)-1].Env["STRIPE_PK"]; leaked {
		t.Fatal("a build queued before secrets were withdrawn still got them")
	}
}

// Pull requests draw on a preview budget of their own: the production branch still deploys.
func TestPreviewBuildsDoNotSpendTheProductionBudget(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{PerHour: 2}, sitebuilds.PreviewModeBranches)
	for i, branch := range []string{"feat/a", "feat/b"} {
		if _, err := h.Push(branch, []string{featSHA, prSHA}[i], "d-budget-"+branch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Push("main", prNewSHA, "d-budget-main"); err != nil {
		t.Fatal(err)
	}
	for _, build := range h.Builds() {
		if build.Preview == nil && build.CommitSHA == prNewSHA && build.Status != sitebuilds.StatusQueued {
			t.Fatalf("production build = %s (%s); previews spent its budget", build.Status, build.FailureReason)
		}
	}
}

// A commit status is public on a public repository: it names the failure, never the runner's own words.
func TestFailedPreviewStatusCarriesNoRunnerDetail(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	h.Runner.Set(sitebuilds.RunStatus{Phase: sitebuilds.RunFailed, Failure: sitebuilds.FailPlatform,
		Detail: "node pool-7 in registry.internal refused"}, nil)
	if _, err := h.Push("feat/a", featSHA, "d-detail-1"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	statuses := h.GitHub.Statuses(featSHA)
	if len(statuses) != 1 || statuses[0].State != gitprovider.StatusFailure || strings.Contains(statuses[0].Description, "registry.internal") {
		t.Fatalf("statuses = %+v", statuses)
	}
	if onlyPreview(t, h).Status != sites.PreviewFailed {
		t.Fatal("the failed build was not recorded on the preview")
	}
}

// Uninstalling the GitHub App takes the previews down with the connections, not 14 days later.
func TestUninstallingTheAppRemovesThePreviews(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	if _, err := h.Push("feat/a", featSHA, "d-uninstall-1"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	body := []byte(fmt.Sprintf(`{"action":"deleted","installation":{"id":%d}}`, h.Installation))
	header := http.Header{"X-Github-Event": {"installation"}, "X-Hub-Signature-256": {h.GitHub.Sign(body)}}
	if _, err := h.Service.HandleWebhook(context.Background(), header, body); err != nil {
		t.Fatal(err)
	}
	if len(h.Publisher.Hosts) != 1 {
		t.Fatalf("hosts = %v; the preview outlived the uninstall", h.Publisher.Hosts)
	}
}
