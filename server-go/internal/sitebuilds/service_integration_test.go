//go:build integration

package sitebuilds_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/githubfake"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

func TestConnect_OnlyWhatTheUsersOwnSignInShows(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	ctx := context.Background()
	_, err := h.Service.Connect(ctx, sitebuilds.ConnectRequest{ProjectID: h.Project, SiteID: h.Site.ID, UserID: h.User,
		Repository: "acme/site", Settings: sitebuilds.Settings{OutputDir: "dist"}})
	if !errors.Is(err, sitebuilds.ErrNotAuthorized) {
		t.Fatalf("connect before signing in: err = %v, want ErrNotAuthorized", err)
	}
	h.SignIn()
	access, err := h.Service.Repositories(ctx, h.User, h.Project, h.Site.ID)
	if err != nil || len(access) != 1 || len(access[0].Repositories) != 1 || access[0].Repositories[0].FullName != "acme/site" {
		t.Fatalf("repositories: %+v %v", access, err)
	}
	// The App covers acme/payroll, but this user's sign-in did not show it.
	_, err = h.Service.Connect(ctx, sitebuilds.ConnectRequest{ProjectID: h.Project, SiteID: h.Site.ID, UserID: h.User,
		RepositoryID: h.PrivateRepo, Settings: sitebuilds.Settings{OutputDir: "dist"}})
	if !errors.Is(err, sitebuilds.ErrNotAuthorized) {
		t.Fatalf("connect a repository the user cannot read: err = %v", err)
	}
	// Another user of the same project has no grant of their own.
	_, err = h.Service.Connect(ctx, sitebuilds.ConnectRequest{ProjectID: h.Project, SiteID: h.Site.ID, UserID: "someone-else",
		Repository: "acme/site", Settings: sitebuilds.Settings{OutputDir: "dist"}})
	if !errors.Is(err, sitebuilds.ErrNotAuthorized) {
		t.Fatalf("another user's sign-in must not carry over: err = %v", err)
	}
	result, err := h.Service.Connect(ctx, sitebuilds.ConnectRequest{ProjectID: h.Project, SiteID: h.Site.ID, UserID: h.User,
		Repository: "acme/site", Settings: sitebuilds.Settings{OutputDir: "dist", Env: map[string]string{"PUBLIC_API_URL": "https://api"}}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	connection := result.Connection
	if connection.RepositoryID != h.SiteRepo || connection.Settings.Branch != "main" || connection.Settings.NodeVersion != "22" {
		t.Errorf("connection %+v", connection)
	}
	if result.Build == nil || result.Build.Status != sitebuilds.StatusQueued || result.Build.CommitSHA != sitebuildstest.HeadSHA || result.Build.Trigger != sitebuilds.TriggerRebuild {
		t.Errorf("first build %+v (%s)", result.Build, result.BuildError)
	}
	stored, err := h.Service.Connection(ctx, h.Project, h.Site.ID)
	if err != nil || stored == nil || stored.Settings.Env["PUBLIC_API_URL"] != "https://api" {
		t.Errorf("stored %+v %v", stored, err)
	}
	if other, err := h.Service.Connection(ctx, "another-project", h.Site.ID); !errors.Is(err, sites.ErrSiteNotFound) || other != nil {
		t.Errorf("another project reads the connection: %+v %v", other, err)
	}
}

func TestSignIn_StateIsSingleUseAndExpires(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	ctx := context.Background()
	started, err := h.Service.StartSignIn(ctx, h.User, h.Project, h.Site.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(started.URL)
	state := parsed.Query().Get("state")
	if parsed.Query().Get("redirect_uri") != sitebuildstest.CallbackURL || len(state) < 40 || len(started.BrowserKey) < 40 || started.BrowserKey == state {
		t.Fatalf("started %+v", started)
	}
	h.GitHub.AddSignIn("c1", map[int64][]int64{h.Installation: {h.SiteRepo}})
	if _, err := h.Service.FinishSignIn(ctx, state, started.BrowserKey, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Service.FinishSignIn(ctx, state, started.BrowserKey, "c1"); !errors.Is(err, sitebuilds.ErrStateInvalid) {
		t.Errorf("reused state: err = %v", err)
	}
	install, _ := h.Service.StartSignIn(ctx, h.User, h.Project, h.Site.ID, true)
	if !strings.Contains(install.URL, "/installations/new?state=") {
		t.Errorf("install link %s", install.URL)
	}
	parsed, _ = url.Parse(install.URL)
	h.Service.SetNow(func() time.Time { return time.Now().Add(11 * time.Minute) })
	if _, err := h.Service.FinishSignIn(ctx, parsed.Query().Get("state"), install.BrowserKey, "c1"); !errors.Is(err, sitebuilds.ErrStateInvalid) {
		t.Errorf("expired state: err = %v", err)
	}
	if _, err := h.Service.StartSignIn(ctx, h.User, "another-project", h.Site.ID, false); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Errorf("sign-in for a site of another project: err = %v", err)
	}
}

// An attacker hands their sign-in link to a victim: GitHub sends the victim's code
// back with the attacker's state, but not the attacker's browser key, so nothing is granted.
func TestSignIn_FinishedInAnotherBrowserGrantsNothing(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	ctx := context.Background()
	started, err := h.Service.StartSignIn(ctx, h.User, h.Project, h.Site.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(started.URL)
	h.GitHub.AddSignIn("victim-code", map[int64][]int64{h.Installation: {h.SiteRepo, h.PrivateRepo}})
	for _, key := range []string{"", "someone-elses-browser"} {
		if _, err := h.Service.FinishSignIn(ctx, parsed.Query().Get("state"), key, "victim-code"); !errors.Is(err, sitebuilds.ErrStateInvalid) {
			t.Errorf("browser key %q: err = %v", key, err)
		}
	}
	access, err := h.Service.Repositories(ctx, h.User, h.Project, h.Site.ID)
	if err != nil || len(access) != 0 {
		t.Fatalf("the victim's repositories were granted: %+v %v", access, err)
	}
	if _, err := h.Service.FinishSignIn(ctx, parsed.Query().Get("state"), started.BrowserKey, "victim-code"); !errors.Is(err, sitebuilds.ErrStateInvalid) {
		t.Errorf("a refused state stays spent: err = %v", err)
	}
}

func TestWebhook_QueuesTheTrackedBranchOnly(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	first := h.Connect().Build
	queued, err := h.Push("feature", "2222222222222222222222222222222222222222", "d-feature")
	if err != nil || queued != 0 {
		t.Fatalf("a push to another branch: queued %d, %v", queued, err)
	}
	queued, err = h.Push("main", "3333333333333333333333333333333333333333", "d-1")
	if err != nil || queued != 1 {
		t.Fatalf("a push to main: queued %d, %v", queued, err)
	}
	queued, err = h.Push("main", "3333333333333333333333333333333333333333", "d-1")
	if err != nil || queued != 0 {
		t.Fatalf("a redelivery queues nothing new: queued %d, %v", queued, err)
	}
	builds := h.Builds()
	if len(builds) != 2 || builds[0].CommitSHA != "3333333333333333333333333333333333333333" || builds[0].Status != sitebuilds.StatusQueued {
		t.Fatalf("builds %+v", builds)
	}
	if superseded := h.Build(first.ID); superseded.Status != sitebuilds.StatusCancelled || !strings.Contains(superseded.FailureReason, "superseded") {
		t.Errorf("the older queued build: %+v", superseded)
	}
	body := []byte(`{"ref":"refs/heads/main"}`)
	_, err = h.Service.HandleWebhook(context.Background(), http.Header{"X-Github-Event": {"push"}, "X-Hub-Signature-256": {"sha256=00"}}, body)
	if !errors.Is(err, gitprovider.ErrBadSignature) {
		t.Errorf("unsigned delivery: err = %v", err)
	}
}

func TestWebhook_RemovedInstallationDisconnects(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	h.Connect()
	body := []byte(fmt.Sprintf(`{"action":"deleted","installation":{"id":%d}}`, h.Installation))
	header := http.Header{"X-Github-Event": {"installation"}, "X-Hub-Signature-256": {h.GitHub.Sign(body)}}
	if _, err := h.Service.HandleWebhook(context.Background(), header, body); err != nil {
		t.Fatal(err)
	}
	if connection, err := h.Service.Connection(context.Background(), h.Project, h.Site.ID); err != nil || connection != nil {
		t.Fatalf("connection after the App was uninstalled: %+v %v", connection, err)
	}
	for _, build := range h.Builds() {
		if build.Status != sitebuilds.StatusCancelled {
			t.Errorf("a build outlived the uninstall: %+v", build)
		}
	}
}

func TestWebhook_RemovedRepositoryOnlyDropsItsConnections(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	h.Connect()
	ctx := context.Background()
	forgotten, err := h.Store.ForgetInstallation(ctx, "github", h.Installation, []int64{h.PrivateRepo})
	if err != nil || forgotten != 0 {
		t.Fatalf("removing another repository forgot %d connections: %v", forgotten, err)
	}
	if connection, err := h.Service.Connection(ctx, h.Project, h.Site.ID); err != nil || connection == nil {
		t.Fatalf("the site's connection must survive another repository's removal: %+v %v", connection, err)
	}
	forgotten, err = h.Store.ForgetInstallation(ctx, "github", h.Installation, []int64{h.SiteRepo})
	if err != nil || forgotten != 1 {
		t.Fatalf("removing the site's repository forgot %d connections: %v", forgotten, err)
	}
}

func TestBuild_DeploysThroughTheSitesPathOnce(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{Secrets: map[string]string{"STRIPE_PK": "pk_live_visible", "DB_PASSWORD": "never-in-a-build"}})
	ctx := context.Background()
	if _, err := h.Service.SetSecretVisibility(ctx, h.Project, []string{"STRIPE_PK"}, h.User); err != nil {
		t.Fatal(err)
	}
	h.Runner.LogText = "== build ==\nusing pk_live_visible to build\n"
	first := h.Connect().Build
	h.RunAll()

	build := h.Build(first.ID)
	if build.Status != sitebuilds.StatusSucceeded || build.DeployID == "" {
		t.Fatalf("build %+v", build)
	}
	site, _ := h.Sites.Get(ctx, h.Project, h.Site.ID)
	if site.LiveDeployID != build.DeployID {
		t.Errorf("the build's deploy is not live: %s vs %s", site.LiveDeployID, build.DeployID)
	}
	deploy, err := h.Sites.GetDeploy(ctx, h.Project, h.Site.ID, build.DeployID)
	if err != nil || !strings.HasPrefix(deploy.Label, "1111111 main") || deploy.CreatedBy != h.User {
		t.Errorf("deploy %+v %v", deploy, err)
	}
	specs := h.Runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("started %d runs", len(specs))
	}
	checkBuildSpec(t, specs[0])
	checkCloneToken(t, h, specs[0])
	sitebuildstest.MustNotFind(t, build.Log, "pk_live_visible")
	if !strings.Contains(build.Log, "[redacted STRIPE_PK]") {
		t.Errorf("log %q", build.Log)
	}
	if len(h.Runner.RemovedIDs()) != 1 || h.Runner.RemovedIDs()[0] != build.ID {
		t.Errorf("the job must be removed: %v", h.Runner.RemovedIDs())
	}
}

// checkBuildSpec: the build-visible secret reaches the build, any other does not, and the run is the plan's.
func checkBuildSpec(t *testing.T, spec sitebuilds.RunSpec) {
	t.Helper()
	if spec.Env["STRIPE_PK"] != "pk_live_visible" {
		t.Error("a build-visible secret must reach the build")
	}
	if _, leaked := spec.Env["DB_PASSWORD"]; leaked {
		t.Error("a secret not marked build-visible reached the build")
	}
	if spec.Image != "node:22-bookworm" || spec.Commit != sitebuildstest.HeadSHA || spec.CloneToken == "" || spec.Memory != "2Gi" ||
		spec.Timeout != 10*time.Minute || !strings.HasSuffix(spec.CloneURL, "/acme/site.git") {
		t.Errorf("spec %+v", spec)
	}
}

// checkCloneToken: the clone token reads one repository only, and went back after the build.
func checkCloneToken(t *testing.T, h *sitebuildstest.Harness, spec sitebuilds.RunSpec) {
	t.Helper()
	issued := h.GitHub.Issued()
	clone := issued[len(issued)-1]
	if len(clone.RepositoryIDs) != 1 || clone.RepositoryIDs[0] != h.SiteRepo || clone.Permissions["contents"] != "read" {
		t.Errorf("clone token %+v", clone)
	}
	if !slices.Contains(h.GitHub.Revoked(), spec.CloneToken) {
		t.Error("the clone token must be revoked once the build is done")
	}
}

func TestBuild_ReportsTheLimitThatStoppedIt(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	// Under gVisor an out-of-memory kill can leave the step exiting 0; the runner reads the kill.
	h.Runner.Outcome = sitebuilds.RunStatus{Phase: sitebuilds.RunFailed, Failure: sitebuilds.FailOutOfMemory, Detail: "the build step ran out of memory"}
	first := h.Connect().Build
	h.RunAll()
	build := h.Build(first.ID)
	if build.Status != sitebuilds.StatusFailed || build.FailureKind != sitebuilds.FailOutOfMemory ||
		!strings.Contains(build.FailureReason, "2Gi of memory") || build.DeployID != "" {
		t.Fatalf("build %+v", build)
	}
	if h.Publisher.Sessions != 0 {
		t.Error("a failed build must deploy nothing")
	}
}

func TestBuild_OutputRefusedByTheUploadChecks(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	h.Runner.Archive = []byte("not an archive at all")
	first := h.Connect().Build
	h.RunAll()
	build := h.Build(first.ID)
	if build.Status != sitebuilds.StatusFailed || build.FailureKind != sitebuilds.FailDeploy || build.FailureReason == "" {
		t.Fatalf("build %+v", build)
	}
}

func TestDispatcher_HoldsTheCaps(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{Slots: 1})
	ctx := context.Background()
	h.Connect()
	// A second site of the same project, built at once.
	second, err := h.Sites.Create(ctx, h.Project, "s2-"+h.Project[5:], nil, "u-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Service.Connect(ctx, sitebuilds.ConnectRequest{ProjectID: h.Project, SiteID: second.ID, UserID: h.User,
		Repository: "acme/site", Settings: sitebuilds.Settings{OutputDir: "dist"}}); err != nil {
		t.Fatal(err)
	}
	holder := "other-replica"
	claimed, err := h.Store.ClaimNext(ctx, 100, holder, time.Now().Add(time.Minute), allKinds)
	if err != nil || claimed == nil {
		t.Fatalf("first claim: %+v %v", claimed, err)
	}
	// The project's cap is 1: the second site's build waits.
	if next, err := h.Store.ClaimNext(ctx, 100, holder, time.Now().Add(time.Minute), allKinds); err != nil || (next != nil && next.ProjectID == h.Project) {
		t.Fatalf("claimed past the project's cap: %+v %v", next, err)
	}
	claimed.Status = sitebuilds.StatusSucceeded
	if err := h.Store.FinishBuild(ctx, claimed, holder); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.FinishBuild(ctx, claimed, holder); !errors.Is(err, sitebuilds.ErrLeaseLost) {
		t.Errorf("finishing twice: err = %v", err)
	}
}

func TestDispatcher_FailsBuildsWhoseReplicaWentAway(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	ctx := context.Background()
	first := h.Connect().Build
	if _, err := h.Store.ClaimNext(ctx, 100, "dead-replica", time.Now().Add(-time.Second), allKinds); err != nil {
		t.Fatal(err)
	}
	h.Dispatcher.Tick(ctx)
	h.Dispatcher.Wait()
	build := h.Build(first.ID)
	if build.Status != sitebuilds.StatusFailed || !strings.Contains(build.FailureReason, "controller stopped") {
		t.Fatalf("build %+v", build)
	}
	removed := false
	for _, id := range h.Runner.RemovedIDs() {
		removed = removed || id == first.ID
	}
	if !removed {
		t.Error("the lost build's job must be removed")
	}
}

func TestBuilds_CountTowardTheHourlyDeploys(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{PerHour: 1})
	ctx := context.Background()
	if first := h.Connect(); first.Build == nil {
		t.Fatalf("the first build fits the hour: %s", first.BuildError)
	}
	if _, err := h.Service.Rebuild(ctx, h.Project, h.Site.ID, h.User); !errors.Is(err, sites.ErrDeployRateLimited) {
		t.Fatalf("a rebuild past the hour: err = %v", err)
	}
	queued, err := h.Push("main", "4444444444444444444444444444444444444444", "d-limited")
	if err != nil || queued != 1 {
		t.Fatalf("push: %d %v", queued, err)
	}
	latest := h.Builds()[0]
	if latest.Status != sitebuilds.StatusFailed || latest.FailureKind != sitebuilds.FailRateLimited {
		t.Fatalf("a push past the hour is recorded, not dropped: %+v", latest)
	}
	h.RunAll()
	if attempts := h.Builds(); attempts[len(attempts)-1].Status != sitebuilds.StatusSucceeded {
		t.Fatalf("the first build's own deploy must not count twice: %+v", attempts[len(attempts)-1])
	}
}

func TestSecretVisibility(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{Secrets: map[string]string{"A_KEY": "a", "B_KEY": "b"}})
	ctx := context.Background()
	listed, err := h.Service.SecretVisibility(ctx, h.Project)
	if err != nil || len(listed) != 2 || listed[0].BuildVisible || listed[1].BuildVisible {
		t.Fatalf("secrets start hidden from builds: %+v %v", listed, err)
	}
	if _, err := h.Service.SetSecretVisibility(ctx, h.Project, []string{"NOPE"}, h.User); err == nil {
		t.Error("an unknown secret must be refused")
	}
	listed, err = h.Service.SetSecretVisibility(ctx, h.Project, []string{"B_KEY"}, h.User)
	if err != nil || listed[0].BuildVisible || !listed[1].BuildVisible {
		t.Fatalf("after marking B_KEY: %+v %v", listed, err)
	}
	listed, _ = h.Service.SetSecretVisibility(ctx, h.Project, nil, h.User)
	if listed[1].BuildVisible {
		t.Error("an empty set hides every secret again")
	}
}

func TestSingleHostAndOffAnswerByName(t *testing.T) {
	singleHost := sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeSingleHost})
	if _, err := singleHost.ListBuilds(context.Background(), "p", "s"); !errors.Is(err, sitebuilds.ErrSingleHost) {
		t.Errorf("single-host: %v", err)
	}
	off := sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{})
	if _, err := off.HandleWebhook(context.Background(), nil, nil); !errors.Is(err, sitebuilds.ErrUnavailable) {
		t.Errorf("off: %v", err)
	}
}

// A replayed signed push for a commit the branch has moved past builds nothing.
func TestWebhook_IgnoresAPushForAStaleHead(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	h.Connect()
	if _, err := h.Push("main", "5555555555555555555555555555555555555555", "d-new"); err != nil {
		t.Fatal(err)
	}
	before := len(h.Builds())
	queued, err := h.Deliver("main", sitebuildstest.HeadSHA, "d-replayed")
	if err != nil || queued != 0 || len(h.Builds()) != before {
		t.Fatalf("a stale push queued %d (%v)", queued, err)
	}
}

// A redelivery neither builds again nor spends another of the hour's deploys.
func TestWebhook_RedeliveryKeepsTheHourlyDeploys(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{PerHour: 3})
	h.Connect()
	for _, delivery := range []string{"d-1", "d-1", "d-1"} {
		if _, err := h.Push("main", "6666666666666666666666666666666666666666", delivery); err != nil {
			t.Fatal(err)
		}
	}
	queued, err := h.Push("main", "7777777777777777777777777777777777777777", "d-2")
	if err != nil || queued != 1 || h.Builds()[0].Status != sitebuilds.StatusQueued {
		t.Fatalf("the third deploy of the hour was spent on redeliveries: %d %v %+v", queued, err, h.Builds()[0])
	}
}

// A build whose status can never be read fails once its time is up, freeing the slots.
func TestDispatcher_GivesUpOnABuildTheClusterNeverAnswers(t *testing.T) {
	limits := sitebuildstest.FreeLimits
	limits.TimeoutSeconds = 1
	h := sitebuildstest.New(t, sitebuildstest.Options{Limits: limits})
	h.Runner.Set(sitebuilds.RunStatus{Phase: sitebuilds.RunPending}, errors.New("the server is currently unable to handle the request"))
	first := h.Connect().Build
	h.RunAll()
	build := h.Build(first.ID)
	if build.Status != sitebuilds.StatusFailed || build.FailureKind != sitebuilds.FailPlatform || !strings.Contains(build.FailureReason, "did not finish") {
		t.Fatalf("build %+v", build)
	}
}

// Shutting down hands a running build back to the queue for another replica, after a short grace.
func TestDispatcher_ShutdownRequeuesRunningBuilds(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	h.Runner.Set(sitebuilds.RunStatus{Phase: sitebuilds.RunRunning}, nil)
	first := h.Connect().Build
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.Dispatcher.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for h.Build(first.ID).Status != sitebuilds.StatusRunning && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited on the running build")
	}
	if build := h.Build(first.ID); build.Status != sitebuilds.StatusQueued {
		t.Fatalf("build %+v", build)
	}
	if removed := h.Runner.RemovedIDs(); len(removed) == 0 || removed[len(removed)-1] != first.ID {
		t.Errorf("the interrupted job must be removed: %v", removed)
	}
}

// A transferred repository is cloned by its id's current name, never by a stale
// name someone else may now own.
func TestBuild_ClonesATransferredRepositoryByID(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	ctx := context.Background()
	h.Connect()
	h.RunAll()
	h.GitHub.RenameRepo(h.SiteRepo, "neworg/site")
	h.GitHub.AddInstallation(h.Installation+5, "acme", &githubfake.Repo{ID: h.SiteRepo + 5, FullName: "acme/site", DefaultBranch: "main",
		Branches: map[string]string{"main": "9999999999999999999999999999999999999999"}})
	build, err := h.Service.Rebuild(ctx, h.Project, h.Site.ID, h.User)
	if err != nil {
		t.Fatal(err)
	}
	if build.Repository != "neworg/site" || build.CommitSHA != sitebuildstest.HeadSHA {
		t.Fatalf("build %+v", build)
	}
	h.RunAll()
	specs := h.Runner.Specs()
	if clone := specs[len(specs)-1].CloneURL; !strings.HasSuffix(clone, "/neworg/site.git") {
		t.Errorf("cloned %s", clone)
	}
	if connection, _ := h.Service.Connection(ctx, h.Project, h.Site.ID); connection.Repository != "neworg/site" {
		t.Errorf("the connection keeps the old name: %+v", connection)
	}
}

// A repository renamed after its build was queued is still cloned under its current name.
func TestBuild_PrepareReadsTheCurrentName(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	h.Connect()
	h.GitHub.RenameRepo(h.SiteRepo, "acme/site-renamed")
	h.RunAll()
	specs := h.Runner.Specs()
	if clone := specs[len(specs)-1].CloneURL; !strings.HasSuffix(clone, "/acme/site-renamed.git") {
		t.Errorf("cloned %s", clone)
	}
}

// Disconnecting stops the site's builds: the queued ones never run, a running one never deploys.
func TestDisconnect_CancelsQueuedAndRunningBuilds(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{Slots: 1})
	ctx := context.Background()
	h.Runner.Set(sitebuilds.RunStatus{Phase: sitebuilds.RunRunning}, nil)
	running := h.Connect().Build
	h.Dispatcher.Tick(ctx)
	deadline := time.Now().Add(5 * time.Second)
	// RUNNING is written at the claim; the Job itself starts after the source checks.
	for len(h.Runner.Specs()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := h.Push("main", "8888888888888888888888888888888888888888", "d-queued"); err != nil {
		t.Fatal(err)
	}
	if err := h.Service.Disconnect(ctx, h.Project, h.Site.ID); err != nil {
		t.Fatal(err)
	}
	h.Runner.Set(sitebuilds.RunStatus{Phase: sitebuilds.RunReady}, nil)
	h.Dispatcher.Wait()
	for _, build := range h.Builds() {
		if build.Status != sitebuilds.StatusCancelled {
			t.Errorf("build %s is %s after disconnect", build.ID, build.Status)
		}
	}
	if h.Publisher.Sessions != 0 {
		t.Error("a build of a disconnected repository deployed")
	}
	if removed := h.Runner.RemovedIDs(); !slices.Contains(removed, running.ID) {
		t.Errorf("the running job must be removed: %v", removed)
	}
}

// A member who lost the developer rung (or the project) stops being the identity
// builds run under: their connection goes inactive and its queued builds are cancelled.
func TestConnector_LosingTheRoleStopsTheirConnection(t *testing.T) {
	members := &sitebuildstest.FakeMembers{}
	h := sitebuildstest.New(t, sitebuildstest.Options{Members: members})
	ctx := context.Background()
	queued := h.Connect().Build
	connection, err := h.Service.Connection(ctx, h.Project, h.Site.ID)
	if err != nil || connection.ConnectedByName != "name of "+h.User || connection.InactiveReason != "" {
		t.Fatalf("connection %+v %v", connection, err)
	}
	members.Remove(h.User)

	connection, err = h.Service.Connection(ctx, h.Project, h.Site.ID)
	if err != nil || connection.InactiveReason == "" {
		t.Fatalf("still active after the connector left: %+v %v", connection, err)
	}
	if build := h.Build(queued.ID); build.Status != sitebuilds.StatusCancelled {
		t.Errorf("their queued build survived: %+v", build)
	}
	if queued, err := h.Push("main", "abababababababababababababababababababab", "d-after"); err != nil || queued != 0 {
		t.Errorf("a push built under the departed member: %d %v", queued, err)
	}
	if _, err := h.Service.Rebuild(ctx, h.Project, h.Site.ID, "another-developer"); !errors.Is(err, sitebuilds.ErrConnectionInactive) {
		t.Errorf("rebuild: %v", err)
	}
}

// A queued build whose connector left before it ran is refused at claim time, never cloned.
func TestConnector_LeavingBeforeTheBuildRunsStopsIt(t *testing.T) {
	members := &sitebuildstest.FakeMembers{}
	h := sitebuildstest.New(t, sitebuildstest.Options{Members: members})
	queued := h.Connect().Build
	members.Remove(h.User)
	h.RunAll()
	if build := h.Build(queued.ID); build.Status == sitebuilds.StatusSucceeded || len(h.Runner.Specs()) != 0 {
		t.Fatalf("built under a departed member: %+v", build)
	}
}

// The clone token goes back as soon as the clone step ends, not after the whole build.
func TestBuild_RevokesTheCloneTokenOnceCloned(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	ctx := context.Background()
	h.Runner.Set(sitebuilds.RunStatus{Phase: sitebuilds.RunRunning, CloneDone: true}, nil)
	first := h.Connect().Build
	h.Dispatcher.Tick(ctx)
	deadline := time.Now().Add(5 * time.Second)
	revoked := func() bool {
		specs := h.Runner.Specs()
		return len(specs) == 1 && slices.Contains(h.GitHub.Revoked(), specs[0].CloneToken)
	}
	for !revoked() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !revoked() || h.Build(first.ID).Status != sitebuilds.StatusRunning {
		t.Fatalf("the clone token outlived the clone step (status %s)", h.Build(first.ID).Status)
	}
	h.Runner.Set(sitebuilds.RunStatus{Phase: sitebuilds.RunReady}, nil)
	h.Dispatcher.Wait()
}

// A connector removed while their build runs: the finished output is not deployed as them.
func TestConnector_RemovedMidBuildDeploysNothing(t *testing.T) {
	members := &sitebuildstest.FakeMembers{}
	h := sitebuildstest.New(t, sitebuildstest.Options{Members: members})
	ctx := context.Background()
	h.Runner.Set(sitebuilds.RunStatus{Phase: sitebuilds.RunRunning, CloneDone: true}, nil)
	running := h.Connect().Build
	h.Dispatcher.Tick(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for len(h.Runner.Specs()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	members.Remove(h.User)
	h.Runner.Set(sitebuilds.RunStatus{Phase: sitebuilds.RunReady}, nil)
	h.Dispatcher.Wait()
	build := h.Build(running.ID)
	if build.Status == sitebuilds.StatusSucceeded || build.DeployID != "" {
		t.Fatalf("deployed as a removed member: %+v", build)
	}
	if !strings.Contains(build.FailureReason, "no longer") {
		t.Errorf("failure reason %q should say the connector lost access", build.FailureReason)
	}
}
