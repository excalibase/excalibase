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
	"github.com/excalibase/provisioning-poc/internal/testutil/githubfake"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

const (
	featSHA  = "2222222222222222222222222222222222222222"
	prSHA    = "3333333333333333333333333333333333333333"
	prNewSHA = "4444444444444444444444444444444444444444"
)

// previewHarness connects the site, runs its first production build and sets the preview mode.
func previewHarness(t *testing.T, options sitebuildstest.Options, mode string) *sitebuildstest.Harness {
	t.Helper()
	options.Previews = true
	h := sitebuildstest.New(t, options)
	h.Connect()
	h.RunAll()
	if mode != sitebuilds.PreviewModeOff {
		if _, err := h.Service.SetPreviewMode(context.Background(), h.Project, h.Site.ID, mode); err != nil {
			t.Fatalf("set preview mode: %v", err)
		}
	}
	return h
}

func previews(t *testing.T, h *sitebuildstest.Harness) []*sites.Preview {
	t.Helper()
	overview, err := h.Service.Previews(context.Background(), h.Project, h.Site.ID)
	if err != nil {
		t.Fatalf("previews: %v", err)
	}
	return overview.Previews
}

func onlyPreview(t *testing.T, h *sitebuildstest.Harness) *sites.Preview {
	t.Helper()
	listed := previews(t, h)
	if len(listed) != 1 {
		t.Fatalf("previews = %+v, want one", listed)
	}
	return listed[0]
}

func TestBranchPushBuildsAPreviewBesideTheLiveSite(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	live, _ := h.Sites.Get(context.Background(), h.Project, h.Site.ID)
	queued, err := h.Push("feat/header", featSHA, "d-feat-1")
	if err != nil || queued != 1 {
		t.Fatalf("push to feat/header queued %d, %v", queued, err)
	}
	h.RunAll()
	preview := onlyPreview(t, h)
	if preview.Status != sites.PreviewReady || preview.Source != sites.PreviewSourceBranch || preview.Ref != "feat/header" || preview.DeployedSHA != featSHA {
		t.Fatalf("preview = %+v", preview)
	}
	if h.Publisher.Hosts[preview.Hostname] != sites.PreviewScriptName(preview) {
		t.Fatalf("hosts = %v", h.Publisher.Hosts)
	}
	after, _ := h.Sites.Get(context.Background(), h.Project, h.Site.ID)
	if after.LiveDeployID != live.LiveDeployID {
		t.Fatal("a preview build changed the live site")
	}
	build := h.Builds()[0]
	if build.Trigger != sitebuilds.TriggerPreview || build.PreviewID != preview.ID || build.Status != sitebuilds.StatusSucceeded || build.DeployID != "" {
		t.Fatalf("build = %+v", build)
	}
	statuses := h.GitHub.Statuses(featSHA)
	if len(statuses) != 1 || statuses[0].State != gitprovider.StatusSuccess || statuses[0].TargetURL != "" || !strings.Contains(statuses[0].Context, h.Site.Name) {
		t.Fatalf("statuses on a public repository = %+v; want success without the link", statuses)
	}
}

func TestPushToTheProductionBranchNeverMakesAPreview(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	if _, err := h.Push("main", featSHA, "d-main-2"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	if got := previews(t, h); len(got) != 0 {
		t.Fatalf("previews = %+v", got)
	}
	if build := h.Builds()[0]; build.PreviewID != "" || build.Trigger != sitebuilds.TriggerPush {
		t.Fatalf("build = %+v", build)
	}
}

func TestPreviewsOffBuildNothingButProduction(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeOff)
	if queued, err := h.Push("feat/header", featSHA, "d-off-1"); err != nil || queued != 0 {
		t.Fatalf("queued %d, %v", queued, err)
	}
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(5, true, prSHA, h.SiteRepo))
	if queued, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 5, prSHA, h.SiteRepo, "d-off-2"); err != nil || queued != 0 {
		t.Fatalf("queued %d, %v", queued, err)
	}
}

func TestDeletedBranchRemovesItsPreviewButAReplayDoesNot(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	if _, err := h.Push("feat/header", featSHA, "d-del-1"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	preview := onlyPreview(t, h)
	if _, err := h.DeliverDeleted("feat/header", "d-del-2"); err != nil {
		t.Fatal(err)
	}
	if len(previews(t, h)) != 1 {
		t.Fatal("a delete event for a branch that still exists removed its preview")
	}
	h.GitHub.DeleteBranch(h.SiteRepo, "feat/header")
	if _, err := h.DeliverDeleted("feat/header", "d-del-3"); err != nil {
		t.Fatal(err)
	}
	if got := previews(t, h); len(got) != 0 {
		t.Fatalf("previews = %+v", got)
	}
	if _, held := h.Publisher.Hosts[preview.Hostname]; held {
		t.Fatal("the preview host is still routed")
	}
}

func pullRequest(number int, open bool, sha string, headRepo int64) githubfake.PullRequest {
	return githubfake.PullRequest{Number: number, Open: open, HeadSHA: sha, HeadRepoID: headRepo}
}

func TestPullRequestPreviewIsRebuiltOnPushAndRemovedOnClose(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModePullRequests)
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(12, true, prSHA, h.SiteRepo))
	if queued, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 12, prSHA, h.SiteRepo, "d-pr-1"); err != nil || queued != 1 {
		t.Fatalf("opened queued %d, %v", queued, err)
	}
	h.RunAll()
	first := onlyPreview(t, h)
	if first.Source != sites.PreviewSourcePullRequest || first.Ref != "12" || first.Status != sites.PreviewReady {
		t.Fatalf("preview = %+v", first)
	}
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(12, true, prNewSHA, h.SiteRepo))
	if _, err := h.DeliverPullRequest(gitprovider.PullRequestSynchronize, 12, prNewSHA, h.SiteRepo, "d-pr-2"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	second := onlyPreview(t, h)
	if second.ID != first.ID || second.Hostname != first.Hostname || second.DeployedSHA != prNewSHA {
		t.Fatalf("after a push the preview is %+v, was %+v", second, first)
	}
	// A pushed branch of the PR does not make a second preview in pull request mode.
	if queued, _ := h.Push("feat/pr", prNewSHA, "d-pr-3"); queued != 0 {
		t.Fatalf("a branch push in pull request mode queued %d", queued)
	}
	if _, err := h.DeliverPullRequest(gitprovider.PullRequestClosed, 12, prNewSHA, h.SiteRepo, "d-pr-4"); err != nil {
		t.Fatal(err)
	}
	if len(previews(t, h)) != 1 {
		t.Fatal("a close event for a pull request still open removed its preview")
	}
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(12, false, prNewSHA, h.SiteRepo))
	if _, err := h.DeliverPullRequest(gitprovider.PullRequestClosed, 12, prNewSHA, h.SiteRepo, "d-pr-5"); err != nil {
		t.Fatal(err)
	}
	if got := previews(t, h); len(got) != 0 {
		t.Fatalf("previews after close = %+v", got)
	}
}

func TestReplayedPullRequestEventIsIgnored(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModePullRequests)
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(8, true, prNewSHA, h.SiteRepo))
	if queued, err := h.DeliverPullRequest(gitprovider.PullRequestSynchronize, 8, prSHA, h.SiteRepo, "d-replay-1"); err != nil || queued != 0 {
		t.Fatalf("an event for an old head queued %d, %v", queued, err)
	}
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(8, false, prSHA, h.SiteRepo))
	if queued, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 8, prSHA, h.SiteRepo, "d-replay-2"); err != nil || queued != 0 {
		t.Fatalf("an opened event for a closed pull request queued %d, %v", queued, err)
	}
}

func TestForkPullRequestsAreNotBuiltUnlessOptedInAndThenNeverSeeSecrets(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{Secrets: map[string]string{"STRIPE_PK": "pk_live_secret"}}, sitebuilds.PreviewModePullRequests)
	ctx := context.Background()
	if _, err := h.Service.SetSecretVisibility(ctx, h.Project, []string{"STRIPE_PK"}, h.User); err != nil {
		t.Fatal(err)
	}
	h.GitHub.SetPullRequest(h.SiteRepo, pullRequest(21, true, prSHA, forkRepo))
	if queued, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 21, prSHA, forkRepo, "d-fork-1"); err != nil || queued != 0 {
		t.Fatalf("a fork pull request queued %d by default, %v", queued, err)
	}
	if _, err := h.Service.SetPreviewAccess(ctx, h.Project, h.Site.ID, sitebuilds.PreviewAccess{Forks: true, Secrets: true}); err != nil {
		t.Fatal(err)
	}
	if queued, err := h.DeliverPullRequest(gitprovider.PullRequestOpened, 21, prSHA, forkRepo, "d-fork-2"); err != nil || queued != 1 {
		t.Fatalf("an allowed fork pull request queued %d, %v", queued, err)
	}
	h.RunAll()
	specs := h.Runner.Specs()
	if _, leaked := specs[len(specs)-1].Env["STRIPE_PK"]; leaked {
		t.Fatal("a fork's preview build was given a project secret")
	}
	if onlyPreview(t, h).Status != sites.PreviewReady {
		t.Fatal("the fork's preview did not deploy")
	}
}

func TestSameRepositoryPreviewsGetSecretsOnlyWhenOptedIn(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{Secrets: map[string]string{"STRIPE_PK": "pk_live_secret"}}, sitebuilds.PreviewModeBranches)
	ctx := context.Background()
	if _, err := h.Service.SetSecretVisibility(ctx, h.Project, []string{"STRIPE_PK"}, h.User); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Push("feat/a", featSHA, "d-sec-1"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	specs := h.Runner.Specs()
	if _, leaked := specs[len(specs)-1].Env["STRIPE_PK"]; leaked {
		t.Fatal("a preview build saw a secret without the opt-in")
	}
	if _, err := h.Service.SetPreviewAccess(ctx, h.Project, h.Site.ID, sitebuilds.PreviewAccess{Secrets: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Push("feat/a", prSHA, "d-sec-2"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	specs = h.Runner.Specs()
	if specs[len(specs)-1].Env["STRIPE_PK"] != "pk_live_secret" {
		t.Fatal("the opt-in did not give the preview build its secret")
	}
}

func TestPreviewPastThePlanIsAFailedBuildWithTheReason(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{MaxPreviews: 1}, sitebuilds.PreviewModeBranches)
	if _, err := h.Push("feat/a", featSHA, "d-cap-1"); err != nil {
		t.Fatal(err)
	}
	if queued, err := h.Push("feat/b", prSHA, "d-cap-2"); err != nil || queued != 1 {
		t.Fatalf("queued %d, %v; want the refusal recorded", queued, err)
	}
	refused := h.Builds()[0]
	if refused.Status != sitebuilds.StatusFailed || refused.FailureKind != sitebuilds.FailPreviewLimit || !strings.Contains(refused.FailureReason, "previews") {
		t.Fatalf("refused build = %+v", refused)
	}
	if statuses := h.GitHub.Statuses(prSHA); len(statuses) != 1 || statuses[0].State != gitprovider.StatusFailure {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestPreviewBuildDoesNotSupersedeAQueuedProductionBuild(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	production, err := h.Service.Rebuild(context.Background(), h.Project, h.Site.ID, h.User)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Push("feat/a", featSHA, "d-sup-1"); err != nil {
		t.Fatal(err)
	}
	if still := h.Build(production.ID); still.Status != sitebuilds.StatusQueued {
		t.Fatalf("production build = %s after a preview push", still.Status)
	}
}

func TestPublicLinksOptInPutsTheURLOnTheStatus(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	if _, err := h.Service.SetPreviewAccess(context.Background(), h.Project, h.Site.ID, sitebuilds.PreviewAccess{PublicLinks: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Push("feat/a", featSHA, "d-link-1"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	preview := onlyPreview(t, h)
	if statuses := h.GitHub.Statuses(featSHA); len(statuses) != 1 || statuses[0].TargetURL != preview.URL() {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestTurningPreviewsOffOrDisconnectingRemovesThem(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	ctx := context.Background()
	if _, err := h.Push("feat/a", featSHA, "d-rm-1"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	if _, err := h.Service.SetPreviewMode(ctx, h.Project, h.Site.ID, sitebuilds.PreviewModeOff); err != nil {
		t.Fatal(err)
	}
	if got := previews(t, h); len(got) != 0 {
		t.Fatalf("previews after turning them off = %+v", got)
	}
	if _, err := h.Service.SetPreviewMode(ctx, h.Project, h.Site.ID, sitebuilds.PreviewModeBranches); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Push("feat/a", prSHA, "d-rm-2"); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	if err := h.Service.Disconnect(ctx, h.Project, h.Site.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.Publisher.Hosts) != 1 {
		t.Fatalf("hosts after disconnect = %v; want only the site's own", h.Publisher.Hosts)
	}
}

func TestPreviewSettingsAreRefusedWithoutAConnectionOrWithABadMode(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{Previews: true})
	ctx := context.Background()
	if _, err := h.Service.SetPreviewMode(ctx, h.Project, h.Site.ID, sitebuilds.PreviewModeBranches); !errors.Is(err, sitebuilds.ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
	h.Connect()
	var input *sitebuilds.InputError
	if _, err := h.Service.SetPreviewMode(ctx, h.Project, h.Site.ID, "tags"); !errors.As(err, &input) {
		t.Fatalf("err = %v, want an InputError", err)
	}
	overview, err := h.Service.Previews(ctx, h.Project, h.Site.ID)
	if err != nil || overview.Settings.Mode != sitebuilds.PreviewModeOff || overview.Max != sites.DefaultLimits.MaxPreviews {
		t.Fatalf("overview = %+v, %v", overview, err)
	}
}

func TestPreviewsAreUnavailableWithoutTheFlag(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	if _, err := h.Service.Previews(context.Background(), h.Project, h.Site.ID); !errors.Is(err, sitebuilds.ErrPreviewsOff) {
		t.Fatalf("err = %v, want ErrPreviewsOff", err)
	}
}
