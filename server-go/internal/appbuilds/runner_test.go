package appbuilds

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

type recordingCluster struct {
	sitebuilds.JobControl
	started []RunSpec
}

func (r *recordingCluster) Start(_ context.Context, spec RunSpec) error {
	r.started = append(r.started, spec)
	return spec.Validate()
}

func queuedBuild() *sitebuilds.Build {
	return &sitebuilds.Build{ID: "b1", Kind: sitebuilds.KindApp, AppID: "app-1", ProjectID: "proj-1", CommitSHA: strings.Repeat("a", 40),
		Limits: sitebuilds.Limits{CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi", TimeoutSeconds: 900},
		App: &sitebuilds.AppBuild{AppSettings: sitebuilds.AppSettings{Builder: sitebuilds.AppBuilderAuto, RootDir: "api",
			DockerfilePath: "Dockerfile.prod", BuildArgs: map[string]string{"MODE": "prod"}}, MaxImageBytes: 2 << 30}}
}

// The dispatcher's prepared run becomes the cluster's spec: the resolved builder, secrets apart from the plain arguments.
func TestRunner_StartsTheSpecTheRunNames(t *testing.T) {
	cluster := &recordingCluster{}
	runner := NewRunner(cluster)
	run := sitebuilds.AppRun{Build: queuedBuild(), Builder: sitebuilds.AppBuilderDockerfile, CloneURL: "https://github.com/acme/api.git",
		CloneToken: "clone-token", Secrets: map[string]string{"API_KEY": "secret"},
		LargestPlan: sitebuilds.BuildSize{CPUMillis: 4000, Memory: "8Gi", Disk: "20Gi"}}
	if err := runner.StartApp(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	spec := cluster.started[0]
	if spec.Builder != BuilderDockerfile || spec.BuildID != "b1" || spec.AppID != "app-1" || spec.ProjectID != "proj-1" ||
		spec.RootDir != "api" || spec.DockerfilePath != "Dockerfile.prod" || spec.BuildArgs["MODE"] != "prod" ||
		spec.Secrets["API_KEY"] != "secret" || spec.CloneToken != "clone-token" || spec.Timeout != 15*time.Minute ||
		spec.MaxImageBytes != 2<<30 || spec.LargestPlan.Memory != "8Gi" || spec.Memory != "4Gi" {
		t.Errorf("spec %+v", spec)
	}
	run.Builder = sitebuilds.AppBuilderAuto
	if err := runner.StartApp(context.Background(), run); err == nil {
		t.Error("an unresolved builder reached the cluster")
	}
}

func TestRunner_WordsFailuresInThePlansNumbers(t *testing.T) {
	runner := NewRunner(&recordingCluster{})
	message := runner.FailureMessage(sitebuilds.RunStatus{Phase: sitebuilds.RunFailed, Failure: FailNoDockerfile}, queuedBuild())
	if message != "no Dockerfile at api/Dockerfile.prod" {
		t.Errorf("message %q", message)
	}
	if message := runner.FailureMessage(sitebuilds.RunStatus{Failure: sitebuilds.FailTimeout}, queuedBuild()); !strings.Contains(message, "15 minutes") {
		t.Errorf("timeout %q", message)
	}
}
