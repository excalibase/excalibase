//go:build integration

package sitebuilds_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

var allKinds = []string{sitebuilds.KindSite, sitebuilds.KindApp}

const (
	checkViolation      pq.ErrorCode = "23514"
	foreignKeyViolation pq.ErrorCode = "23503"
)

// queueAppBuild writes a QUEUED build of the app straight to the store.
func queueAppBuild(t *testing.T, h *sitebuildstest.Harness, project, appID, delivery string, limits sitebuilds.Limits) *sitebuilds.Build {
	t.Helper()
	build := &sitebuilds.Build{ID: uuid.NewString(), Kind: sitebuilds.KindApp, AppID: appID, ProjectID: project, Status: sitebuilds.StatusQueued,
		Trigger: sitebuilds.TriggerPush, CommitSHA: sitebuildstest.HeadSHA, DeliveryID: delivery, Limits: limits, RequestedBy: h.User,
		Provider: "github", InstallationID: h.Installation, RepositoryID: h.SiteRepo, Repository: "acme/site", CreatedAt: time.Now().UTC(),
		App: &sitebuilds.AppBuild{AppSettings: sitebuilds.AppSettings{Branch: "main", Builder: sitebuilds.AppBuilderBuildpacks}, MaxImageBytes: 1 << 30,
			MaxBuildsPerHour: sitebuildstest.FreeAppLimits.MaxBuildsPerHour}}
	if created, err := h.Store.Enqueue(context.Background(), build, 30); err != nil || !created {
		t.Fatalf("queue an app build: created %v, %v", created, err)
	}
	return build
}

func claim(t *testing.T, h *sitebuildstest.Harness, slots int) *sitebuilds.Build {
	t.Helper()
	build, err := h.Store.ClaimNext(context.Background(), slots, "replica-"+h.Project, time.Now().Add(time.Minute), allKinds)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return build
}

func finish(t *testing.T, h *sitebuildstest.Harness, build *sitebuilds.Build) {
	t.Helper()
	build.Status = sitebuilds.StatusSucceeded
	if err := h.Store.FinishBuild(context.Background(), build, "replica-"+h.Project); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func count(t *testing.T, h *sitebuildstest.Harness, query string, args ...any) int {
	t.Helper()
	var rows int
	if err := h.SQL().QueryRow(query, args...).Scan(&rows); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return rows
}

func TestMigration_AppGitBuilds(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	appID := h.AddApp(h.Project)
	const insert = `INSERT INTO site_builds (id, kind, site_id, app_id, project_id, status, trigger, commit_sha, settings,
		cpu_millis, memory, disk, timeout_seconds, project_concurrency, requested_by)
		VALUES ($1, $2, $3, $4, $5, 'QUEUED', 'push', 'x', '{}', 1, '1Gi', '1Gi', 1, 1, 'u')`
	cases := []struct {
		name, kind string
		siteID     any
		appID      any
		project    string
		code       pq.ErrorCode
	}{
		{"an app build naming a site too", "app", h.Site.ID, appID, h.Project, checkViolation},
		{"an app build naming no app", "app", nil, nil, h.Project, checkViolation},
		{"a site build naming an app", "site", nil, appID, h.Project, checkViolation},
		{"an unknown kind", "worker", h.Site.ID, nil, h.Project, checkViolation},
		{"an app of another project", "app", nil, appID, "proj-other", foreignKeyViolation},
	}
	for _, tc := range cases {
		_, err := h.SQL().Exec(insert, uuid.NewString(), tc.kind, tc.siteID, tc.appID, tc.project)
		var refused *pq.Error
		if !errors.As(err, &refused) || refused.Code != tc.code {
			t.Errorf("%s: %v, want code %s", tc.name, err, tc.code)
		}
	}
	if count(t, h, `SELECT COUNT(*) FROM tier_configs WHERE app_build_max_per_hour <> max_site_deploys_per_hour`) != 0 {
		t.Error("each plan's app builds per hour must start at its site deploys per hour")
	}
	if count(t, h, `SELECT COUNT(*) FROM site_builds WHERE kind = 'site' AND site_id IS NULL`) != 0 {
		t.Error("existing rows must keep their site")
	}
	checkDownAndUp(t, h)
}

// checkDownAndUp runs the down migration then the up one in a transaction it rolls back, leaving the shared database as it was.
func checkDownAndUp(t *testing.T, h *sitebuildstest.Harness) {
	t.Helper()
	read := func(direction string) string {
		raw, err := os.ReadFile("../storage/postgres/migrations/000110_app_git_builds." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	tx, err := h.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(read("down")); err != nil {
		t.Fatalf("down: %v", err)
	}
	var columns int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE (table_name = 'site_builds' AND column_name IN ('kind', 'app_id'))
		OR (table_name = 'tier_configs' AND column_name = 'app_build_max_per_hour') OR table_name = 'app_build_attempts'`).
		Scan(&columns); err != nil || columns != 0 {
		t.Errorf("down left %d columns: %v", columns, err)
	}
	if _, err := tx.Exec(read("up")); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// One shared slot: a running site build blocks an app build, and the app runs once the slot frees.
func TestClaim_SitesAndAppsShareTheSlots(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	h.Connect()
	appID := h.AddApp(h.Project)
	queueAppBuild(t, h, h.Project, appID, "", sitebuildstest.FreeAppLimits.Limits)
	site := claim(t, h, 1)
	if site == nil || site.Kind != sitebuilds.KindSite {
		t.Fatalf("the older site build goes first: %+v", site)
	}
	if next := claim(t, h, 1); next != nil {
		t.Fatalf("a running site build holds the only slot, yet %+v was claimed", next)
	}
	if next, _ := h.Store.ClaimNext(context.Background(), 1, "r", time.Now().Add(time.Minute), []string{sitebuilds.KindSite}); next != nil {
		t.Fatalf("site-only claim took %+v", next)
	}
	finish(t, h, site)
	app := claim(t, h, 1)
	if app == nil || app.Kind != sitebuilds.KindApp || app.AppID != appID || app.App == nil || app.App.MaxImageBytes != 1<<30 {
		t.Fatalf("the app build after the slot freed: %+v", app)
	}
}

func TestClaim_OneRunningBuildPerAppAndTheProjectsAppCap(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	first, second := h.AddApp(h.Project), h.AddApp(h.Project)
	capTwo := sitebuildstest.FreeAppLimits.Limits
	capTwo.ProjectConcurrency = 2
	queueAppBuild(t, h, h.Project, first, "", capTwo)
	running := claim(t, h, 100)
	if running == nil || running.AppID != first {
		t.Fatalf("claimed %+v", running)
	}
	queueAppBuild(t, h, h.Project, first, "", capTwo)
	if next := claim(t, h, 100); next != nil {
		t.Fatalf("a second build of a running app was claimed: %+v", next)
	}
	queueAppBuild(t, h, h.Project, second, "", sitebuildstest.FreeAppLimits.Limits)
	if next := claim(t, h, 100); next != nil {
		t.Fatalf("the project's app cap of one was passed: %+v", next)
	}
	// The app cap counts app builds only: the project's site build still runs.
	h.Connect()
	if site := claim(t, h, 100); site == nil || site.Kind != sitebuilds.KindSite {
		t.Fatalf("a site build waits on the app cap: %+v", site)
	}
	finish(t, h, running)
	if next := claim(t, h, 100); next == nil || next.Kind != sitebuilds.KindApp {
		t.Fatalf("once the app finished: %+v", next)
	}
}

func TestEnqueue_AnAppDeliveryQueuesOnceAndSupersedes(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	appID, other := h.AddApp(h.Project), h.AddApp(h.Project)
	ctx := context.Background()
	first := queueAppBuild(t, h, h.Project, appID, "delivery-1", sitebuildstest.FreeAppLimits.Limits)
	again := *first
	again.ID = uuid.NewString()
	if created, err := h.Store.Enqueue(ctx, &again, 30); err != nil || created {
		t.Errorf("the same delivery for the same app: created %v, %v", created, err)
	}
	queueAppBuild(t, h, h.Project, other, "delivery-1", sitebuildstest.FreeAppLimits.Limits)
	newer := queueAppBuild(t, h, h.Project, appID, "delivery-2", sitebuildstest.FreeAppLimits.Limits)
	target := sitebuilds.Target{Kind: sitebuilds.KindApp, ProjectID: h.Project, ID: appID}
	builds, err := h.Store.ListBuilds(ctx, target, 10)
	if err != nil || len(builds) != 2 || builds[0].ID != newer.ID || builds[1].Status != sitebuilds.StatusCancelled {
		t.Fatalf("the app's builds: %+v %v", builds, err)
	}
	if queued, err := h.Store.DeliveryQueued(ctx, target, "delivery-1"); err != nil || !queued {
		t.Errorf("delivery-1 queued: %v %v", queued, err)
	}
	otherProject := sitebuilds.Target{Kind: sitebuilds.KindApp, ProjectID: "proj-elsewhere", ID: appID}
	if got, err := h.Store.GetBuild(ctx, otherProject, newer.ID); err != nil || got != nil {
		t.Errorf("another project reads the build: %+v %v", got, err)
	}
	if foreign, err := h.Store.ListBuilds(ctx, otherProject, 10); err != nil || len(foreign) != 0 {
		t.Errorf("another project lists %d builds: %v", len(foreign), err)
	}
	stray := *newer
	stray.ID, stray.ProjectID, stray.DeliveryID = uuid.NewString(), "proj-elsewhere", ""
	if _, err := h.Store.Enqueue(ctx, &stray, 30); !errors.Is(err, sitebuilds.ErrNotConnected) {
		t.Errorf("a build of an app the project does not hold: %v", err)
	}
}

func TestAppDeleted_TakesItsConnectionAndBuilds(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	appID := h.AddApp(h.Project)
	result := h.ConnectApp(appID, sitebuilds.AppSettings{})
	if result.Build == nil || result.Build.Kind != sitebuilds.KindApp {
		t.Fatalf("connect queued %+v (%s)", result.Build, result.BuildError)
	}
	if _, err := h.SQL().Exec(`DELETE FROM apps WHERE project_id = $1 AND id = $2`, h.Project, appID); err != nil {
		t.Fatal(err)
	}
	if left := count(t, h, `SELECT COUNT(*) FROM app_git_connections WHERE project_id = $1 AND app_id = $2`, h.Project, appID); left != 0 {
		t.Errorf("%d connections outlived the app", left)
	}
	if left := count(t, h, `SELECT COUNT(*) FROM site_builds WHERE project_id = $1 AND app_id = $2`, h.Project, appID); left != 0 {
		t.Errorf("%d builds outlived the app", left)
	}
}

// A push builds the site and the app tracking the branch; the app's build is published, never deployed as a site.
func TestWebhook_PushBuildsTheSiteAndTheApp(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true, Secrets: map[string]string{"API_TOKEN": "tok-very-secret-1"}})
	h.Connect()
	appID := h.AddApp(h.Project)
	h.ConnectApp(appID, sitebuilds.AppSettings{BuildArgs: map[string]string{"MODE": "prod"}})
	if _, err := h.Service.SetSecretVisibility(context.Background(), h.Project, []string{"API_TOKEN"}, h.User); err != nil {
		t.Fatal(err)
	}
	h.RunAll()
	const pushed = "2222222222222222222222222222222222222222"
	if queued, err := h.Push("main", pushed, "push-1"); err != nil || queued != 2 {
		t.Fatalf("push queued %d: %v", queued, err)
	}
	h.RunAll()
	builds, err := h.Service.ListAppBuilds(context.Background(), h.Project, appID)
	if err != nil || len(builds) != 2 || builds[0].Status != sitebuilds.StatusSucceeded || builds[0].CommitSHA != pushed {
		t.Fatalf("app builds %+v %v", builds, err)
	}
	if !strings.HasPrefix(builds[0].DeployID, "deploy-of-") {
		t.Errorf("deploy id %q", builds[0].DeployID)
	}
	runs := h.AppRunner.Runs()
	last := runs[len(runs)-1]
	if last.Builder != sitebuilds.AppBuilderBuildpacks || last.Secrets["API_TOKEN"] != "tok-very-secret-1" || last.Build.App.BuildArgs["MODE"] != "prod" {
		t.Errorf("run %+v", last)
	}
	if forgotten, err := h.Store.ForgetInstallation(context.Background(), "github", h.Installation, nil); err != nil || forgotten != 2 {
		t.Errorf("forgetting the installation dropped %d connections: %v", forgotten, err)
	}
	if connection, err := h.Service.AppConnection(context.Background(), h.Project, appID); err != nil || connection != nil {
		t.Errorf("the app's connection outlived its installation: %+v %v", connection, err)
	}
}

func TestAppBuilds_OffQueueNothing(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	h.Connect()
	appID := h.AddApp(h.Project)
	if _, err := h.Service.ConnectApp(context.Background(), sitebuilds.AppConnectRequest{ProjectID: h.Project, AppID: appID,
		UserID: h.User, Repository: "acme/site"}); !errors.Is(err, sitebuilds.ErrAppBuildsOff) {
		t.Fatalf("connect with the flag off: %v", err)
	}
	if _, err := h.Service.AppConnection(context.Background(), h.Project, "missing"); !errors.Is(err, sitebuilds.ErrAppBuildsOff) {
		t.Errorf("read with the flag off: %v", err)
	}
	on := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	if _, err := on.Service.AppConnection(context.Background(), on.Project, "missing"); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Errorf("an app the project does not hold: %v", err)
	}
}

// enqueueAt queues an app build of trigger at a moment, under a plan allowing perHour app builds.
func enqueueAt(h *sitebuildstest.Harness, project, appID, trigger, delivery string, at time.Time, perHour int) (*sitebuilds.Build, bool, error) {
	build := &sitebuilds.Build{ID: uuid.NewString(), Kind: sitebuilds.KindApp, AppID: appID, ProjectID: project, Status: sitebuilds.StatusQueued,
		Trigger: trigger, CommitSHA: sitebuildstest.HeadSHA, DeliveryID: delivery, Limits: sitebuildstest.FreeAppLimits.Limits, RequestedBy: h.User,
		Provider: "github", InstallationID: h.Installation, RepositoryID: h.SiteRepo, Repository: "acme/site", CreatedAt: at,
		App: &sitebuilds.AppBuild{AppSettings: sitebuilds.AppSettings{Branch: "main", Builder: sitebuilds.AppBuilderBuildpacks},
			MaxImageBytes: 1 << 30, MaxBuildsPerHour: perHour}}
	created, err := h.Store.Enqueue(context.Background(), build, 30)
	return build, created, err
}

// The project's app builds per hour are counted as they queue: a redelivery or a refused enqueue takes no slot.
func TestEnqueue_CountsTheProjectsAppBuildsInTheLastHour(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	// Postgres keeps microseconds; a finer now would shave the wait below the hour's edge.
	appID, now := h.AddApp(h.Project), time.Now().UTC().Truncate(time.Microsecond)
	queued := func(trigger, delivery string, at time.Time) *sitebuilds.Build {
		t.Helper()
		build, created, err := enqueueAt(h, h.Project, appID, trigger, delivery, at, 2)
		if err != nil || !created {
			t.Fatalf("%s at %v: created %v, %v", delivery, at, created, err)
		}
		return build
	}
	queued(sitebuilds.TriggerPush, "d-1", now)
	for range 3 {
		if _, created, err := enqueueAt(h, h.Project, appID, sitebuilds.TriggerPush, "d-1", now, 2); err != nil || created {
			t.Fatalf("a redelivery: created %v, %v", created, err)
		}
	}
	if _, _, err := enqueueAt(h, "proj-elsewhere", appID, sitebuilds.TriggerPush, "", now, 2); !errors.Is(err, sitebuilds.ErrNotConnected) {
		t.Fatalf("an app the project does not hold: %v", err)
	}
	if second := queued(sitebuilds.TriggerPush, "d-2", now.Add(time.Minute)); second.Status != sitebuilds.StatusQueued {
		t.Fatalf("the second build in the hour: %+v", second)
	}
	late := queued(sitebuilds.TriggerPush, "d-3", now.Add(2*time.Minute))
	if late.Status != sitebuilds.StatusFailed || late.FailureKind != sitebuilds.FailRateLimited {
		t.Fatalf("a push past the cap is kept as failed: %+v", late)
	}
	var limited *sitebuilds.AppBuildRateLimitError
	_, _, err := enqueueAt(h, h.Project, appID, sitebuilds.TriggerRebuild, "", now.Add(2*time.Minute), 2)
	if !errors.As(err, &limited) || limited.RetryAfter != 58*time.Minute {
		t.Fatalf("a rebuild past the cap: %v", err)
	}
	other := "proj-other-" + h.Project
	if build, _, err := enqueueAt(h, other, h.AddApp(other), sitebuilds.TriggerPush, "", now, 2); err != nil || build.Status != sitebuilds.StatusQueued {
		t.Errorf("another project shares the count: %+v %v", build, err)
	}
	if later := queued(sitebuilds.TriggerRebuild, "", now.Add(61*time.Minute)); later.Status != sitebuilds.StatusQueued {
		t.Errorf("an hour later: %+v", later)
	}
}

// A push past the plan's app builds per hour is kept as FAILED and never runs.
func TestWebhook_APushPastTheAppBuildsPerHourIsKept(t *testing.T) {
	limits := sitebuildstest.FreeAppLimits
	limits.MaxBuildsPerHour = 1
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true, AppLimits: limits})
	appID := h.AddApp(h.Project)
	h.ConnectApp(appID, sitebuilds.AppSettings{})
	if _, err := h.Push("main", "3333333333333333333333333333333333333333", "push-late"); err != nil {
		t.Fatal(err)
	}
	builds, err := h.Service.ListAppBuilds(context.Background(), h.Project, appID)
	if err != nil || len(builds) != 2 || builds[0].Status != sitebuilds.StatusFailed || builds[0].FailureKind != sitebuilds.FailRateLimited {
		t.Fatalf("builds %+v %v", builds, err)
	}
}

// With the flag off a queued app build is cancelled by name, and its app can still be disconnected.
func TestAppBuilds_SwitchedOffCancelQueuedAndStillDisconnect(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{})
	appID := h.AddApp(h.Project)
	queued := queueAppBuild(t, h, h.Project, appID, "", sitebuildstest.FreeAppLimits.Limits)
	h.RunAll()
	target := sitebuilds.Target{Kind: sitebuilds.KindApp, ProjectID: h.Project, ID: appID}
	build, err := h.Store.GetBuild(context.Background(), target, queued.ID)
	if err != nil || build.Status != sitebuilds.StatusCancelled || !strings.Contains(build.FailureReason, "switched off") {
		t.Fatalf("queued app build with the flag off: %+v %v", build, err)
	}
	if len(h.AppRunner.Runs()) != 0 {
		t.Error("an app build ran with the flag off")
	}
	if _, err := h.SQL().Exec(`INSERT INTO app_git_connections (project_id, app_id, provider, installation_id, repository_id, repository,
		branch, connected_by) VALUES ($1, $2, 'github', 1, 1, 'acme/site', 'main', 'u')`, h.Project, appID); err != nil {
		t.Fatal(err)
	}
	if err := h.Service.DisconnectApp(context.Background(), h.Project, appID); err != nil {
		t.Fatalf("disconnect with the flag off: %v", err)
	}
	if left := count(t, h, `SELECT COUNT(*) FROM app_git_connections WHERE project_id = $1`, h.Project); left != 0 {
		t.Errorf("%d connections left", left)
	}
}
