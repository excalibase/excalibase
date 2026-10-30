package sitebuilds

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

func queueApp(t *testing.T, bed *testBed, settings AppSettings) *Build {
	t.Helper()
	bed.connectApp(settings)
	build, err := bed.service.RebuildApp(context.Background(), testProject, testApp, testUser)
	if err != nil {
		t.Fatalf("queue an app build: %v", err)
	}
	return build
}

func queueSite(t *testing.T, bed *testBed) *Build {
	t.Helper()
	bed.connectSite()
	build, err := bed.service.Rebuild(context.Background(), testProject, testSite, testUser)
	if err != nil {
		t.Fatalf("queue a site build: %v", err)
	}
	return build
}

func TestAppBuild_SecretsAndArgumentsReachTheRunnerAndNeverTheLog(t *testing.T) {
	bed := newTestBed(true)
	bed.store.visible = []string{"API_KEY"}
	bed.apps.logText = "== docker build ==\nusing sk-live-very-secret with arg plain-but-hidden\n"
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderDockerfile, RootDir: "api", DockerfilePath: "Dockerfile.prod",
		BuildArgs: map[string]string{"FLAVOUR": "plain-but-hidden"}})
	bed.runAll()
	runs := bed.apps.started()
	if len(runs) != 1 {
		t.Fatalf("started %d app builds", len(runs))
	}
	run := runs[0]
	if run.Builder != AppBuilderDockerfile || run.Secrets["API_KEY"] != "sk-live-very-secret" || run.Build.App.BuildArgs["FLAVOUR"] != "plain-but-hidden" ||
		run.CloneToken != "clone-token-1" || run.CloneURL != "https://github.test/acme/api.git" || run.LargestPlan.Memory != "8Gi" {
		t.Errorf("run %+v", run)
	}
	if _, leaked := run.Build.App.BuildArgs["API_KEY"]; leaked {
		t.Error("a secret was passed as a build argument")
	}
	done := bed.store.build(queued.ID)
	if done.Status != StatusSucceeded || done.DeployID == "" {
		t.Fatalf("outcome %+v", done)
	}
	for _, hidden := range []string{"sk-live-very-secret", "plain-but-hidden"} {
		if strings.Contains(done.Log, hidden) || strings.Contains(bed.store.logs[queued.ID], hidden) {
			t.Errorf("the log shows %q: %q", hidden, done.Log)
		}
	}
	published := bed.publisher.published
	if len(published) != 1 || published[0] != (AppPublish{BuildID: queued.ID, RunID: queued.ID, ProjectID: testProject, AppID: testApp,
		CommitSHA: testCommit, MaxImageBytes: testAppLimits.MaxImageBytes}) {
		t.Errorf("published %+v", published)
	}
}

// Builder auto looks for the Dockerfile in the commit before anything runs, and never guesses.
func TestAppBuild_AutoPicksTheBuilderFromTheCommit(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]bool
		filesErr error
		want     string
	}{
		{name: "a Dockerfile", files: map[string]bool{"api/Dockerfile": true}, want: AppBuilderDockerfile},
		{name: "none", files: map[string]bool{"Dockerfile": true}, want: AppBuilderBuildpacks},
		{name: "the host did not answer", filesErr: errors.New("GitHub answered 502")},
	}
	for _, tc := range cases {
		bed := newTestBed(true)
		bed.provider.files, bed.provider.filesErr = tc.files, tc.filesErr
		queued := queueApp(t, bed, AppSettings{RootDir: "api"})
		bed.runAll()
		done, runs := bed.store.build(queued.ID), bed.apps.started()
		if tc.want == "" {
			if done.Status != StatusFailed || done.FailureKind != FailPlatform || len(runs) != 0 || bed.provider.minted != 0 {
				t.Errorf("%s: %+v, %d runs, %d tokens", tc.name, done, len(runs), bed.provider.minted)
			}
			continue
		}
		if len(runs) != 1 || runs[0].Builder != tc.want || !slices.Equal(bed.provider.checked, []string{"api/Dockerfile"}) {
			t.Errorf("%s: runs %+v, checked %v", tc.name, runs, bed.provider.checked)
		}
	}
}

func TestAppBuild_PublishFailuresKeepTheirKind(t *testing.T) {
	cases := []struct {
		err      error
		wantKind FailureKind
	}{
		{&BuildFailure{Kind: "image-size", Err: errors.New("the built image is larger than this plan allows")}, "image-size"},
		{errors.New("registry unreachable at 10.0.0.1"), FailDeploy},
	}
	for _, tc := range cases {
		bed := newTestBed(true)
		bed.publisher.err = tc.err
		queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
		bed.runAll()
		done := bed.store.build(queued.ID)
		if done.Status != StatusFailed || done.FailureKind != tc.wantKind || strings.Contains(done.FailureReason, "10.0.0.1") {
			t.Errorf("%v: %+v", tc.err, done)
		}
	}
}

func TestAppBuild_WithNoPublisherFailsBeforeRunning(t *testing.T) {
	bed := newTestBed(true)
	bed.dispatcher.EnableApps(bed.apps, nil)
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	done := bed.store.build(queued.ID)
	if done.Status != StatusFailed || done.FailureKind != FailPlatform || !strings.Contains(done.FailureReason, "publish") ||
		len(bed.apps.started()) != 0 {
		t.Errorf("outcome %+v", done)
	}
}

func TestAppBuild_FailureWordedByTheAppRunner(t *testing.T) {
	bed := newTestBed(true)
	bed.apps.outcomes = []RunStatus{{Phase: RunFailed, Failure: "no-buildpack"}}
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	done := bed.store.build(queued.ID)
	if done.FailureKind != "no-buildpack" || done.FailureReason != "the app build failed: no-buildpack" || len(bed.publisher.published) != 0 {
		t.Errorf("outcome %+v", done)
	}
}

// Once the clone is done its credential goes: the token is revoked once and the Secret deleted, for both kinds.
func TestBuild_ForgetsTheCloneCredentialOnceCloned(t *testing.T) {
	for _, kind := range []string{KindSite, KindApp} {
		bed := newTestBed(true)
		jobs := bed.jobsOf(kind)
		jobs.forgetErr = errors.New("the API server is busy")
		queued := bed.queue(t, kind)
		bed.runAll()
		if revoked := bed.provider.revokedTokens(); !slices.Equal(revoked, []string{"clone-token-1"}) {
			t.Errorf("%s: revoked %v, want the one token once", kind, revoked)
		}
		if forgot := jobs.forgotIDs(); !slices.Equal(forgot, []string{queued.ID}) {
			t.Errorf("%s: forgot %v", kind, forgot)
		}
		if done := bed.store.build(queued.ID); done.Status != StatusSucceeded {
			t.Errorf("%s: a clone secret that would not delete failed the build: %+v", kind, done)
		}
	}
}

// runsc sometimes refuses to start a step; the build runs once more with a fresh credential, and only once.
func TestBuild_RetriesOnceWhenTheSandboxDidNotStart(t *testing.T) {
	notStarted := RunStatus{Phase: RunFailed, Failure: FailPlatform, ExitCode: 128, StartFailed: true, Detail: "the sandbox could not start the build step"}
	for _, kind := range []string{KindSite, KindApp} {
		bed := newTestBed(true)
		jobs := bed.jobsOf(kind)
		jobs.outcomes = []RunStatus{notStarted, {Phase: RunReady}}
		queued := bed.queue(t, kind)
		bed.runAll()
		done := bed.store.build(queued.ID)
		if done.Status != StatusSucceeded || !strings.Contains(done.Log, "retried") {
			t.Errorf("%s: %+v", kind, done)
		}
		if starts := bed.starts(kind); len(starts) != 2 || starts[0] == starts[1] {
			t.Errorf("%s: the retry must use a fresh clone credential: %v", kind, starts)
		}
		retryID := queued.ID + "-r1"
		if removed := jobs.removedIDs(); !slices.Equal(removed, []string{queued.ID, retryID}) {
			t.Errorf("%s: each attempt's job must be removed: %v", kind, removed)
		}
		if read := jobs.statusIDs[len(jobs.statusIDs)-1]; read != retryID {
			t.Errorf("%s: the status read after the retry follows %q, want %q", kind, read, retryID)
		}
		if kind == KindApp && (bed.apps.started()[1].RunID != retryID || bed.publisher.published[0].RunID != retryID) {
			t.Errorf("the retry runs and publishes as %s: %+v %+v", retryID, bed.apps.started()[1], bed.publisher.published)
		}
		if kind == KindSite && (bed.sites.started()[1].BuildID != retryID || !slices.Equal(bed.sites.collected, []string{retryID})) {
			t.Errorf("the site retry runs and collects as %s: %+v %v", retryID, bed.sites.started()[1].BuildID, bed.sites.collected)
		}

		again := newTestBed(true)
		again.jobsOf(kind).outcomes = []RunStatus{notStarted, notStarted}
		queued = again.queue(t, kind)
		again.runAll()
		done = again.store.build(queued.ID)
		if done.Status != StatusFailed || done.FailureKind != FailPlatform || len(again.starts(kind)) != 2 {
			t.Errorf("%s: a second start error: %+v, %d starts", kind, done, len(again.starts(kind)))
		}
	}
}

// With the flag off the dispatcher claims site builds only, so a queued app build never runs.
func TestDispatcher_ClaimsAppBuildsOnlyWithTheFlagOn(t *testing.T) {
	for _, appsOn := range []bool{true, false} {
		bed := newTestBed(appsOn)
		bed.store.builds = append(bed.store.builds, &Build{ID: "app-build-waiting", Kind: KindApp, AppID: testApp,
			ProjectID: testProject, Status: StatusQueued, App: &AppBuild{}})
		bed.dispatcher.Tick(context.Background())
		bed.dispatcher.Wait()
		kinds := bed.store.claimedKinds[0]
		if appsOn != slices.Contains(kinds, KindApp) || !slices.Contains(kinds, KindSite) {
			t.Errorf("apps on %v: claimed kinds %v", appsOn, kinds)
		}
		waiting := bed.store.build("app-build-waiting")
		if !appsOn && (waiting.Status != StatusCancelled || !strings.Contains(waiting.FailureReason, "switched off")) {
			t.Errorf("a queued app build with the flag off must be cancelled by name: %+v", waiting)
		}
	}
	unwired := newTestBed(true)
	unwired.dispatcher.apps = nil
	unwired.dispatcher.Tick(context.Background())
	if kinds := unwired.store.claimedKinds[0]; !slices.Equal(kinds, []string{KindSite}) {
		t.Errorf("a dispatcher with no app runner claimed %v", kinds)
	}
}

func (b *testBed) jobsOf(kind string) *fakeJobs {
	if kind == KindApp {
		return &b.apps.fakeJobs
	}
	return &b.sites.fakeJobs
}

func (b *testBed) queue(t *testing.T, kind string) *Build {
	if kind == KindApp {
		return queueApp(t, b, AppSettings{Builder: AppBuilderBuildpacks})
	}
	return queueSite(t, b)
}

// starts answers each start's clone token.
func (b *testBed) starts(kind string) []string {
	var tokens []string
	if kind == KindApp {
		for _, run := range b.apps.started() {
			tokens = append(tokens, run.CloneToken)
		}
		return tokens
	}
	for _, spec := range b.sites.started() {
		tokens = append(tokens, spec.CloneToken)
	}
	return tokens
}

func TestDispatcher_RemovesLostBuildsWithTheirOwnRunner(t *testing.T) {
	bed := newTestBed(true)
	bed.store.lost = []LostBuild{{ID: "lost-site", Kind: KindSite}, {ID: "lost-app", Kind: KindApp}}
	bed.runAll()
	if removed := bed.sites.removedIDs(); !slices.Equal(removed, []string{"lost-site", "lost-site-r1"}) {
		t.Errorf("site runner removed %v", removed)
	}
	if removed := bed.apps.removedIDs(); !slices.Equal(removed, []string{"lost-app", "lost-app-r1"}) {
		t.Errorf("app runner removed %v", removed)
	}
	unwired := newTestBed(true)
	unwired.dispatcher.apps = nil
	unwired.store.lost = []LostBuild{{ID: "lost-app", Kind: KindApp}}
	unwired.runAll()
	if removed := unwired.sites.removedIDs(); len(removed) != 0 {
		t.Errorf("an app build's job was removed through the site runner: %v", removed)
	}
}

// An app build reaching a replica with no app runner stops before anything is minted.
func TestAppBuild_OnAReplicaWithoutAnAppRunnerStopsCleanly(t *testing.T) {
	bed := newTestBed(true)
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.dispatcher.apps = nil
	claimed, _ := bed.store.ClaimNext(context.Background(), 1, "replica", time.Now(), []string{KindApp})
	bed.dispatcher.run(context.Background(), claimed)
	done := bed.store.build(queued.ID)
	if done.Status != StatusFailed || done.FailureKind != FailPlatform || !strings.Contains(done.FailureReason, "not wired") || bed.provider.minted != 0 {
		t.Errorf("outcome %+v, %d tokens", done, bed.provider.minted)
	}
	var kind buildKind = unwiredApps{}
	if kind.jobs() != nil || kind.preflight(context.Background()) == nil || kind.start(context.Background(), gitprovider.CloneCredential{}) == nil ||
		kind.deliver(context.Background()).status != StatusFailed || kind.failureMessage(RunStatus{}) == "" {
		t.Error("an unwired app kind must refuse every step")
	}
}

// A repository renamed since it was connected is cloned under its current name, the app's connection updated.
func TestAppBuild_FollowsARenamedRepository(t *testing.T) {
	bed := newTestBed(true)
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.provider.repo.FullName = "acme/api-renamed"
	bed.runAll()
	runs := bed.apps.started()
	if len(runs) != 1 || runs[0].CloneURL != "https://github.test/acme/api-renamed.git" {
		t.Fatalf("runs %+v", runs)
	}
	if stored, _ := bed.store.GetAppConnection(context.Background(), testProject, testApp); stored.Repository != "acme/api-renamed" {
		t.Errorf("the connection kept the old name: %+v", stored)
	}
	if done := bed.store.build(queued.ID); done.Status != StatusSucceeded {
		t.Errorf("outcome %+v", done)
	}
}

func TestBuildFailure_KeepsItsCause(t *testing.T) {
	cause := errors.New("the built image is larger than this plan allows")
	var err error = &BuildFailure{Kind: "image-size", Err: cause}
	if !errors.Is(err, cause) || err.Error() != cause.Error() {
		t.Errorf("%v does not unwrap to its cause", err)
	}
}

// A build argument named like a secret must not take the secret's place in the redaction list.
func TestAppBuild_AnArgumentNamedLikeASecretHidesBoth(t *testing.T) {
	bed := newTestBed(true)
	bed.store.visible = []string{"API_KEY"}
	bed.apps.logText = "secret sk-live-very-secret, argument not-the-secret-value\n"
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks, BuildArgs: map[string]string{"API_KEY": "not-the-secret-value"}})
	bed.runAll()
	done := bed.store.build(queued.ID)
	for _, hidden := range []string{"sk-live-very-secret", "not-the-secret-value"} {
		if strings.Contains(done.Log, hidden) {
			t.Errorf("the log shows %q: %q", hidden, done.Log)
		}
	}
}

// A lost build's job is removed under every attempt's name.
func TestDispatcher_RemovesEveryAttemptOfALostBuild(t *testing.T) {
	bed := newTestBed(true)
	bed.store.lost = []LostBuild{{ID: "lost-app", Kind: KindApp}}
	bed.runAll()
	if removed := bed.apps.removedIDs(); !slices.Equal(removed, []string{"lost-app", "lost-app-r1"}) {
		t.Errorf("removed %v", removed)
	}
}

// Past the plan's app builds per hour a rebuild is refused, and a push is kept as FAILED and never started.
func TestAppBuilds_HoldThePlansBuildsPerHour(t *testing.T) {
	bed := newTestBed(true)
	bed.connectApp(AppSettings{Builder: AppBuilderBuildpacks})
	ctx := context.Background()
	for range testAppLimits.MaxBuildsPerHour {
		if _, err := bed.service.RebuildApp(ctx, testProject, testApp, testUser); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bed.service.RebuildApp(ctx, testProject, testApp, testUser); !errors.Is(err, ErrAppBuildRateLimited) ||
		!strings.Contains(err.Error(), "17m") {
		t.Fatalf("a rebuild past the cap: %v", err)
	}
	bed.provider.event = gitprovider.Event{DeliveryID: "d-late", Push: &gitprovider.Push{InstallationID: testInstall,
		RepositoryID: testRepoID, Repository: testRepo, Branch: "main", Commit: bed.provider.head}}
	if _, err := bed.service.HandleWebhook(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	builds := bed.store.queuedOf(KindApp)
	late := builds[len(builds)-1]
	if late.DeliveryID != "d-late" || late.Status != StatusFailed || late.FailureKind != FailRateLimited {
		t.Fatalf("the push past the cap: %+v", late)
	}
	before := len(bed.apps.started())
	bed.store.mu.Lock()
	for _, build := range bed.store.builds {
		if build.Status == StatusQueued {
			build.Status = StatusCancelled
		}
	}
	bed.store.mu.Unlock()
	bed.runAll()
	if len(bed.apps.started()) != before {
		t.Error("a rate-limited build was started")
	}
}

// Disconnecting is never blocked: with the flag off the app's repository is still forgotten and its builds cancelled.
func TestDisconnectApp_WorksWithTheFlagOff(t *testing.T) {
	bed := newTestBed(false)
	bed.connectApp(AppSettings{})
	if err := bed.service.DisconnectApp(context.Background(), testProject, testApp); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if stored, _ := bed.store.GetAppConnection(context.Background(), testProject, testApp); stored != nil {
		t.Errorf("the connection survived: %+v", stored)
	}
	if len(bed.store.cancelled) != 1 {
		t.Errorf("cancelled %v", bed.store.cancelled)
	}
	if err := bed.service.DisconnectApp(context.Background(), "proj-b", testApp); !errors.Is(err, ErrNotConnected) {
		t.Errorf("another project's disconnect: %v", err)
	}
}
