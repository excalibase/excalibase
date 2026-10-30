package appbuilds

import (
	"cmp"
	"context"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// Failure kinds the publish step names (EXC-547 step 3), beside FailImageTooLarge.
const (
	FailRegistryFull sitebuilds.FailureKind = "registry-full"
	FailBadImage     sitebuilds.FailureKind = "bad-image"
)

// Cluster runs app build Jobs: the k8s app build runner.
type Cluster interface {
	sitebuilds.JobControl
	Start(ctx context.Context, spec RunSpec) error
}

// Runner is the build queue's app runner over the cluster; sitebuilds cannot name RunSpec, so the spec is made here.
type Runner struct {
	Cluster
}

var _ sitebuilds.AppRunner = (*Runner)(nil)

func NewRunner(cluster Cluster) *Runner { return &Runner{Cluster: cluster} }

// StartApp starts the run's spec; the cluster refuses a builder still auto.
func (r *Runner) StartApp(ctx context.Context, run sitebuilds.AppRun) error {
	return r.Start(ctx, SpecFor(run))
}

// FailureMessage words a failed build in its plan's own numbers.
func (r *Runner) FailureMessage(status sitebuilds.RunStatus, build *sitebuilds.Build) string {
	return FailureMessage(status, SpecFor(sitebuilds.AppRun{Build: build}))
}

// SpecFor is the cluster's spec of a prepared run: the build's settings and plan limits, with what the dispatcher minted and read.
func SpecFor(run sitebuilds.AppRun) RunSpec {
	build := run.Build
	spec := RunSpec{BuildID: cmp.Or(run.RunID, build.ID), AppID: build.AppID, ProjectID: build.ProjectID, Builder: Builder(run.Builder),
		CloneURL: run.CloneURL, Commit: build.CommitSHA, CloneToken: run.CloneToken, Secrets: run.Secrets,
		CPUMillis: build.Limits.CPUMillis, Memory: build.Limits.Memory, Disk: build.Limits.Disk,
		Timeout: time.Duration(build.Limits.TimeoutSeconds) * time.Second, LargestPlan: run.LargestPlan}
	if settings := build.App; settings != nil {
		spec.RootDir, spec.DockerfilePath, spec.BuildArgs = settings.RootDir, settings.DockerfilePath, settings.BuildArgs
		spec.MaxImageBytes = settings.MaxImageBytes
	}
	return spec
}
