package sitebuilds

import (
	"context"
	"errors"
	"strings"
	"testing"

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
