package sitebuilds

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

func connectRequest(settings Settings) ConnectRequest {
	return ConnectRequest{ProjectID: testProject, SiteID: testSite, UserID: testUser, Repository: testRepo, Settings: settings}
}

func TestConnect_ServerSiteNeedsAFramework(t *testing.T) {
	bed := newTestBed(false)
	bed.service.sites = fakeSites{kind: sites.KindServer}
	_, err := bed.service.Connect(context.Background(), connectRequest(Settings{OutputDir: "dist"}))
	var input *InputError
	if !errors.As(err, &input) || !strings.Contains(input.Reason, "framework") {
		t.Fatalf("a server site built without a framework: err = %v", err)
	}
}

func TestConnect_StaticSiteRefusesAFramework(t *testing.T) {
	bed := newTestBed(false)
	bed.service.sites = fakeSites{kind: sites.KindStatic}
	_, err := bed.service.Connect(context.Background(), connectRequest(Settings{Framework: FrameworkNextjs}))
	var input *InputError
	if !errors.As(err, &input) || !strings.Contains(input.Reason, "server") {
		t.Fatalf("a static site given a framework: err = %v", err)
	}
}

func TestConnect_NextjsServerSiteQueuesAFirstBuildWithItsFramework(t *testing.T) {
	bed := newTestBed(false)
	bed.service.sites = fakeSites{kind: sites.KindServer}
	result, err := bed.service.Connect(context.Background(), connectRequest(Settings{Framework: FrameworkNextjs}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connection.Settings.Framework != FrameworkNextjs || result.Build == nil || result.Build.Settings.Framework != FrameworkNextjs {
		t.Errorf("connection %+v build %+v (%s)", result.Connection, result.Build, result.BuildError)
	}
}

func TestFailureMessage_FrameworkRefusalPointsToAContainer(t *testing.T) {
	message := failureMessage(RunStatus{Failure: FailFramework, ExitCode: 13}, Limits{}, Settings{Framework: FrameworkNextjs})
	for _, want := range []string{"Next.js", "app container", "log"} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q lacks %q", message, want)
		}
	}
}

func TestFailureMessage_NamesTheFrameworkOfThePreset(t *testing.T) {
	for framework, label := range map[string]string{FrameworkNuxt: "Nuxt", FrameworkSvelteKit: "SvelteKit", FrameworkAstro: "Astro"} {
		refused := failureMessage(RunStatus{Failure: FailFramework, ExitCode: 13}, Limits{}, Settings{Framework: framework})
		missing := failureMessage(RunStatus{Failure: FailNoOutput}, Limits{}, Settings{Framework: framework})
		if !strings.Contains(refused, label) || !strings.Contains(refused, "app container") || !strings.Contains(missing, label) ||
			strings.Contains(missing, "output directory") || strings.Contains(refused, "Next.js") {
			t.Errorf("%s: refused %q, missing %q", framework, refused, missing)
		}
	}
}

func TestBuild_NextjsFrameworkReachesTheRunner(t *testing.T) {
	bed := newTestBed(false)
	bed.service.sites = fakeSites{kind: sites.KindServer}
	if _, err := bed.service.Connect(context.Background(), connectRequest(Settings{Framework: FrameworkNextjs})); err != nil {
		t.Fatal(err)
	}
	bed.runAll()
	started := bed.sites.started()
	if len(started) != 1 || started[0].Framework != FrameworkNextjs {
		t.Fatalf("the runner was started with %+v", started)
	}
}

// With sitesserver off a server site is as good as absent: every route answers as an unmounted one.
func TestServerSitesOff_ConnectAnswersAsUnmountedAndRunsNothing(t *testing.T) {
	bed := newTestBed(false)
	bed.service.sites = fakeSites{kind: sites.KindServer, serverOff: true}
	_, err := bed.service.Connect(context.Background(), connectRequest(Settings{Framework: FrameworkNextjs}))
	if !errors.Is(err, sites.ErrServerSitesOff) {
		t.Fatalf("connect a server site while they are off: err = %v, want ErrServerSitesOff", err)
	}
	if _, err := bed.service.Rebuild(context.Background(), testProject, testSite, testUser); !errors.Is(err, sites.ErrServerSitesOff) {
		t.Errorf("rebuild: err = %v", err)
	}
	if _, err := bed.service.Connection(context.Background(), testProject, testSite); !errors.Is(err, sites.ErrServerSitesOff) {
		t.Errorf("read the connection: err = %v", err)
	}
}

func TestServerSitesOff_AFrameworkOnAStaticSiteIsAlsoUnmounted(t *testing.T) {
	bed := newTestBed(false)
	bed.service.sites = fakeSites{kind: sites.KindStatic, serverOff: true}
	_, err := bed.service.Connect(context.Background(), connectRequest(Settings{Framework: FrameworkNextjs}))
	if !errors.Is(err, sites.ErrServerSitesOff) {
		t.Fatalf("err = %v: the refusal must not name server sites while they are off", err)
	}
}

func TestServerSitesOff_APushDoesNotBuildTheirConnection(t *testing.T) {
	bed := newTestBed(false)
	bed.service.sites = fakeSites{kind: sites.KindServer}
	bed.store.siteConnections[testSite] = &Connection{SiteID: testSite, GitSource: GitSource{ProjectID: testProject, Provider: "github",
		InstallationID: testInstall, RepositoryID: testRepoID, Repository: testRepo, ConnectedBy: testUser},
		Settings: Settings{Branch: "main", Framework: FrameworkNextjs, NodeVersion: DefaultNodeVersion}}
	bed.service.sites = fakeSites{kind: sites.KindServer, serverOff: true}
	targets, err := bed.service.pushTargets(context.Background(), &gitprovider.Push{InstallationID: testInstall, RepositoryID: testRepoID, Repository: testRepo, Branch: "main"})
	if err != nil || len(targets) != 0 {
		t.Fatalf("targets %v, err %v: a server site must not build while they are off", targets, err)
	}
}

func TestServerSitesOff_AQueuedBuildIsRefusedBeforeItRuns(t *testing.T) {
	bed := newTestBed(false)
	bed.service.sites = fakeSites{kind: sites.KindServer}
	if _, err := bed.service.Connect(context.Background(), connectRequest(Settings{Framework: FrameworkNextjs})); err != nil {
		t.Fatal(err)
	}
	bed.service.sites = fakeSites{kind: sites.KindServer, serverOff: true}
	bed.runAll()
	if started := bed.sites.started(); len(started) != 0 {
		t.Fatalf("the customer's build ran with server sites off: %+v", started)
	}
}

func TestFailureMessage_NextjsWithNoOutputSaysWhatIsMissing(t *testing.T) {
	message := failureMessage(RunStatus{Failure: FailNoOutput}, Limits{}, Settings{Framework: FrameworkNextjs})
	if !strings.Contains(message, "server bundle") || strings.Contains(message, "output directory") {
		t.Errorf("message %q: a Next.js site has no output directory setting to check", message)
	}
	if static := failureMessage(RunStatus{Failure: FailNoOutput}, Limits{}, Settings{OutputDir: "dist"}); !strings.Contains(static, `"dist"`) {
		t.Errorf("static message %q", static)
	}
}

func TestPublicFailure_FrameworkRefusalSaysSo(t *testing.T) {
	if message := publicFailure(FailFramework); !strings.Contains(message, "server-rendered") {
		t.Errorf("message %q", message)
	}
}
