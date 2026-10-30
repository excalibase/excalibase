package sitebuilds

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

func TestAppSettings_Normalise(t *testing.T) {
	settings, err := AppSettings{Branch: " main ", RootDir: "./services/api/", BuildArgs: map[string]string{"NODE_ENV": "production"}}.Normalise()
	if err != nil || settings.Builder != AppBuilderAuto || settings.RootDir != "services/api" || settings.Branch != "main" ||
		settings.BuildArgs["NODE_ENV"] != "production" {
		t.Fatalf("defaults: %+v %v", settings, err)
	}
	refused := map[string]AppSettings{
		"an unknown builder":           {Branch: "main", Builder: "nixpacks"},
		"a root outside the repo":      {Branch: "main", RootDir: "../other"},
		"a Dockerfile outside":         {Branch: "main", DockerfilePath: "/etc/Dockerfile"},
		"the builder's own arguments":  {Branch: "main", BuildArgs: map[string]string{"BUILDKIT_SYNTAX": "x"}},
		"an argument that is no name":  {Branch: "main", BuildArgs: map[string]string{"1BAD": "x"}},
		"a branch a shell reads twice": {Branch: "main;rm"},
	}
	for name, settings := range refused {
		var input *InputError
		if _, err := settings.Normalise(); !errors.As(err, &input) {
			t.Errorf("%s: err = %v, want an InputError", name, err)
		}
	}
}

func TestConnectApp_NeedsTheUsersOwnSignInAndQueuesAFirstBuild(t *testing.T) {
	bed := newTestBed(true)
	ctx := context.Background()
	request := AppConnectRequest{ProjectID: testProject, AppID: testApp, UserID: testUser, Repository: testRepo,
		Settings: AppSettings{Builder: AppBuilderDockerfile, BuildArgs: map[string]string{"GO_VERSION": "1.25"}}}
	if _, err := bed.service.ConnectApp(ctx, AppConnectRequest{ProjectID: testProject, AppID: testApp, UserID: testUser,
		Repository: "acme/payroll"}); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("a repository the sign-in did not show: err = %v", err)
	}
	result, err := bed.service.ConnectApp(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	connection := result.Connection
	if connection.Settings.Branch != "main" || connection.InstallationID != testInstall || connection.ConnectedBy != testUser {
		t.Errorf("connection %+v", connection)
	}
	build := result.Build
	if build == nil || build.Kind != KindApp || build.AppID != testApp || build.SiteID != "" || build.Status != StatusQueued {
		t.Fatalf("first build %+v (%s)", build, result.BuildError)
	}
	if build.Limits != testAppLimits.Limits || build.App == nil || build.App.MaxImageBytes != testAppLimits.MaxImageBytes ||
		build.App.Builder != AppBuilderDockerfile || build.App.BuildArgs["GO_VERSION"] != "1.25" {
		t.Errorf("the app plan's limits and settings are not snapshotted: %+v %+v", build.Limits, build.App)
	}
	stored, err := bed.service.AppConnection(ctx, testProject, testApp)
	if err != nil || stored == nil || stored.Repository != testRepo {
		t.Errorf("stored %+v %v", stored, err)
	}
}

// Every app method is scoped by the project: another project's id never reaches this app.
func TestAppMethods_AreScopedByProject(t *testing.T) {
	bed := newTestBed(true)
	bed.connectApp(AppSettings{})
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["connection"] = bed.service.AppConnection(ctx, "proj-b", testApp)
	_, checks["builds"] = bed.service.ListAppBuilds(ctx, "proj-b", testApp)
	_, checks["build"] = bed.service.GetAppBuild(ctx, "proj-b", testApp, "any")
	_, checks["rebuild"] = bed.service.RebuildApp(ctx, "proj-b", testApp, testUser)
	_, checks["connect"] = bed.service.ConnectApp(ctx, AppConnectRequest{ProjectID: "proj-b", AppID: testApp, UserID: testUser, Repository: testRepo})
	for name, err := range checks {
		if !errors.Is(err, apphost.ErrAppNotFound) {
			t.Errorf("%s from another project: err = %v", name, err)
		}
	}
	if len(bed.store.queuedOf(KindApp)) != 0 {
		t.Error("another project queued a build of this app")
	}
}

func TestAppMethods_RefusedWhileTheFlagIsOff(t *testing.T) {
	bed := newTestBed(false)
	ctx := context.Background()
	if _, err := bed.service.ConnectApp(ctx, AppConnectRequest{ProjectID: testProject, AppID: testApp, UserID: testUser,
		Repository: testRepo}); !errors.Is(err, ErrAppBuildsOff) {
		t.Errorf("connect: err = %v", err)
	}
	if _, err := bed.service.RebuildApp(ctx, testProject, testApp, testUser); !errors.Is(err, ErrAppBuildsOff) {
		t.Errorf("rebuild: err = %v", err)
	}
	unwired := NewService(newMemoryStore(), newFakeProvider(), fakeSites{}, fakeLimits{}, fakeSecrets{}, Options{Mode: ModeOn})
	if _, err := unwired.AppConnection(ctx, testProject, testApp); !errors.Is(err, ErrAppBuildsOff) {
		t.Errorf("an install that never wired app builds: err = %v", err)
	}
}

func TestRebuildAndDisconnectApp(t *testing.T) {
	bed := newTestBed(true)
	ctx := context.Background()
	if _, err := bed.service.RebuildApp(ctx, testProject, testApp, testUser); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("rebuild with nothing connected: err = %v", err)
	}
	bed.connectApp(AppSettings{})
	build, err := bed.service.RebuildApp(ctx, testProject, testApp, testUser)
	if err != nil || build.Trigger != TriggerRebuild || build.CommitSHA != testCommit || build.RequestedBy != testUser {
		t.Fatalf("rebuild %+v %v", build, err)
	}
	builds, err := bed.service.ListAppBuilds(ctx, testProject, testApp)
	if err != nil || len(builds) != 1 {
		t.Fatalf("builds %d %v", len(builds), err)
	}
	if got, err := bed.service.GetAppBuild(ctx, testProject, testApp, build.ID); err != nil || got.ID != build.ID {
		t.Errorf("get %+v %v", got, err)
	}
	if _, err := bed.service.GetAppBuild(ctx, testProject, testApp, "missing"); !errors.Is(err, ErrBuildNotFound) {
		t.Errorf("a missing build: %v", err)
	}
	if err := bed.service.DisconnectApp(ctx, testProject, testApp); err != nil {
		t.Fatal(err)
	}
	appTarget := Target{Kind: KindApp, ProjectID: testProject, ID: testApp}
	if len(bed.store.cancelled) != 1 || bed.store.cancelled[0] != appTarget {
		t.Errorf("disconnect cancels the app's builds: %+v", bed.store.cancelled)
	}
}

// One push to a repository and branch queues the sites and the apps tracking it; apps only with the flag on.
func TestWebhook_FansOutToSitesAndApps(t *testing.T) {
	for _, appsOn := range []bool{true, false} {
		bed := newTestBed(appsOn)
		bed.connectSite()
		bed.connectApp(AppSettings{})
		bed.provider.event = gitprovider.Event{DeliveryID: "d-1", Push: &gitprovider.Push{InstallationID: testInstall,
			RepositoryID: testRepoID, Repository: testRepo, Branch: "main", Commit: bed.provider.head}}
		queued, err := bed.service.HandleWebhook(context.Background(), nil, nil)
		want := 1
		if appsOn {
			want = 2
		}
		if err != nil || queued != want {
			t.Fatalf("apps on %v: queued %d, err %v", appsOn, queued, err)
		}
		apps := bed.store.queuedOf(KindApp)
		if appsOn && (len(apps) != 1 || apps[0].DeliveryID != "d-1" || apps[0].Trigger != TriggerPush || apps[0].RequestedBy != testUser) {
			t.Errorf("the app's build %+v", apps)
		}
		if !appsOn && len(apps) != 0 {
			t.Errorf("an app build was queued with the flag off: %+v", apps)
		}
		if len(bed.store.queuedOf(KindSite)) != 1 {
			t.Errorf("apps on %v: the site's build is missing", appsOn)
		}
		if again, err := bed.service.HandleWebhook(context.Background(), nil, nil); err != nil || again != 0 {
			t.Errorf("a redelivery queued %d: %v", again, err)
		}
	}
}

// A connector who lost the developer rung stops their app connection, as with sites.
func TestConnector_LosingTheRoleStopsTheAppConnection(t *testing.T) {
	bed := newTestBed(true)
	bed.service.options.Members = standings{testUser: false}
	bed.connectApp(AppSettings{})
	_, err := bed.service.RebuildApp(context.Background(), testProject, testApp, testUser)
	if !errors.Is(err, ErrConnectionInactive) {
		t.Fatalf("rebuild: err = %v", err)
	}
	appTarget := Target{Kind: KindApp, ProjectID: testProject, ID: testApp}
	if len(bed.store.deactivated) != 1 || bed.store.deactivated[0] != appTarget || len(bed.store.cancelled) != 1 {
		t.Errorf("deactivated %+v, cancelled %+v", bed.store.deactivated, bed.store.cancelled)
	}
	stored, _ := bed.service.AppConnection(context.Background(), testProject, testApp)
	if stored == nil || !strings.Contains(stored.InactiveReason, "can no longer deploy") {
		t.Errorf("stored %+v", stored)
	}
}

// standings answers each user's developer rung.
type standings map[string]bool

func (s standings) Standing(_ context.Context, userID, _ string) (MemberStanding, error) {
	return MemberStanding{Name: userID, CanDeploy: s[userID]}, nil
}

// A member without the developer rung saves no connection, site or app, before anything is read.
func TestConnect_RefusesAMemberWhoCannotDeploy(t *testing.T) {
	bed := newTestBed(true)
	bed.service.options.Members = standings{testUser: false}
	ctx := context.Background()
	_, err := bed.service.ConnectApp(ctx, AppConnectRequest{ProjectID: testProject, AppID: testApp, UserID: testUser, Repository: testRepo})
	if !errors.Is(err, ErrNotDeveloper) {
		t.Errorf("app: err = %v", err)
	}
	_, err = bed.service.Connect(ctx, ConnectRequest{ProjectID: testProject, SiteID: testSite, UserID: testUser, Repository: testRepo})
	if !errors.Is(err, ErrNotDeveloper) {
		t.Errorf("site: err = %v", err)
	}
	if app, _ := bed.store.GetAppConnection(ctx, testProject, testApp); app != nil || len(bed.store.siteConnections) != 0 {
		t.Errorf("a refused connect saved %+v / %+v", app, bed.store.siteConnections)
	}
	bed.service.options.Members = standings{testUser: true}
	if _, err := bed.service.ConnectApp(ctx, AppConnectRequest{ProjectID: testProject, AppID: testApp, UserID: testUser, Repository: testRepo}); err != nil {
		t.Errorf("a developer connects: %v", err)
	}
}

// An app's sign-in is the sites' flow, bound to the app; the flag and the app are checked first.
func TestStartAppSignIn_BindsTheApp(t *testing.T) {
	ctx := context.Background()
	if _, err := newTestBed(false).service.StartAppSignIn(ctx, testUser, testProject, testApp, false); !errors.Is(err, ErrAppBuildsOff) {
		t.Errorf("flag off: %v", err)
	}
	bed := newTestBed(true)
	if _, err := bed.service.StartAppSignIn(ctx, testUser, testProject, "app-elsewhere", false); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Errorf("an app the project does not hold: %v", err)
	}
	started, err := bed.service.StartAppSignIn(ctx, testUser, testProject, testApp, true)
	if err != nil || started.URL == "" || started.BrowserKey == "" {
		t.Fatalf("started %+v: %v", started, err)
	}
	saved := bed.store.signIns
	if len(saved) != 1 || saved[0].AppID != testApp || saved[0].SiteID != "" || saved[0].ProjectID != testProject || saved[0].UserID != testUser {
		t.Errorf("saved %+v", saved)
	}
	access, err := bed.service.AppRepositories(ctx, testUser, testProject, testApp)
	if err != nil || len(access) != 1 {
		t.Errorf("repositories %+v: %v", access, err)
	}
	if _, err := newTestBed(false).service.AppRepositories(ctx, testUser, testProject, testApp); !errors.Is(err, ErrAppBuildsOff) {
		t.Errorf("repositories with the flag off: %v", err)
	}
}

// A connection can always be removed: site git builds off or single-host never block the disconnect.
func TestDisconnectApp_WhateverTheInstallOffers(t *testing.T) {
	for _, mode := range []Mode{ModeOff, ModeSingleHost} {
		store := newMemoryStore()
		bed := &testBed{store: store}
		bed.connectApp(AppSettings{})
		service := NewService(store, nil, nil, nil, nil, Options{Mode: mode})
		if err := service.DisconnectApp(context.Background(), testProject, testApp); err != nil {
			t.Errorf("mode %v: %v", mode, err)
		}
		if want := (Target{Kind: KindApp, ProjectID: testProject, ID: testApp}); len(store.cancelled) != 1 || store.cancelled[0] != want {
			t.Errorf("mode %v: cancelled %+v", mode, store.cancelled)
		}
	}
	if err := NewService(nil, nil, nil, nil, nil, Options{Mode: ModeOff}).DisconnectApp(context.Background(), testProject, testApp); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no store: %v", err)
	}
}

// Below the developer rung a build argument shows only its name; the copy never touches the original.
func TestWithoutBuildArgValues_KeepsNamesOnly(t *testing.T) {
	args := map[string]string{"NPM_REGISTRY": "https://token@npm.example.com"}
	connection := &AppConnection{AppID: testApp, Settings: AppSettings{Branch: "main", BuildArgs: args}}
	hidden := connection.WithoutBuildArgValues()
	if value, named := hidden.Settings.BuildArgs["NPM_REGISTRY"]; !named || value != "" || hidden.AppID != testApp {
		t.Errorf("hidden connection %+v", hidden)
	}
	build := &Build{ID: "b1", Kind: KindApp, App: &AppBuild{AppSettings: AppSettings{BuildArgs: args}, MaxImageBytes: 7}}
	shown := build.WithoutBuildArgValues()
	if shown.App.BuildArgs["NPM_REGISTRY"] != "" || shown.App.MaxImageBytes != 7 || shown.ID != "b1" {
		t.Errorf("hidden build %+v %+v", shown, shown.App)
	}
	if args["NPM_REGISTRY"] == "" || build.App.BuildArgs["NPM_REGISTRY"] == "" || connection.Settings.BuildArgs["NPM_REGISTRY"] == "" {
		t.Error("the original lost its values")
	}
	site := &Build{ID: "s1", Kind: KindSite}
	if site.WithoutBuildArgValues().App != nil {
		t.Error("a site build has no app settings")
	}
	var none *AppConnection
	if none.WithoutBuildArgValues() != nil {
		t.Error("no connection stays none")
	}
}
