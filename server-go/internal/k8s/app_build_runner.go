package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// Exit codes the app build steps use to name a failure.
const (
	exitNoDockerfile = 12
	// The build steps exit 13 when the root directory or Dockerfile resolves outside the checkout.
	exitOutsideCheckout = 13
	// The CNB lifecycle's detector exits 20 when no buildpack group passes detection.
	exitNoBuildpack = 20
)

// appBuildLogSteps are the steps whose output the history keeps, in order.
var appBuildLogSteps = []buildLogStep{
	{siteBuildFence, "network fence"}, {siteBuildClone, "clone"}, {appBuildBuildKit, "docker build"},
	{appBuildRunImage, "run image"}, {appBuildAnalyze, "analyze"}, {appBuildBuildpacks, "buildpacks"},
	{appBuildExport, "export"}, {siteBuildCollect, "collect"},
}

// AppBuildRunner runs app builds (EXC-547) as Jobs in the app build namespace.
type AppBuildRunner struct {
	*buildJobs
	options AppBuildOptions
}

func NewAppBuildRunner(client *Client, options AppBuildOptions) *AppBuildRunner {
	return &AppBuildRunner{options: options, buildJobs: &buildJobs{
		client: client, namespace: options.Namespace, prefix: "app-build-", secretSuffixes: []string{"-secrets", cloneSecretSuffix},
		steps: appBuildLogSteps, collectStep: siteBuildCollect, archivePath: appBuildArchivePath, stream: client.execStream, component: appBuildComponent, kindOf: appStepFailureKind,
	}}
}

// Start puts the fence, quota and scripts in place, then creates the Job suspended,
// its Secrets owned by it, and resumes it.
func (r *AppBuildRunner) Start(ctx context.Context, spec appbuilds.RunSpec) error {
	if err := spec.Validate(r.options.CloneSchemes...); err != nil {
		return err
	}
	if err := r.ensureEnvironment(ctx, spec.LargestPlan); err != nil {
		return err
	}
	secrets, git := renderAppBuildSecrets(r.options, spec)
	return r.create(ctx, spec.BuildID, renderAppBuildJob(r.options, spec), []*corev1.Secret{secrets, git})
}

// ensureEnvironment converges the namespace's shared objects on every start, so an edit cannot loosen them for long.
func (r *AppBuildRunner) ensureEnvironment(ctx context.Context, largest sitebuilds.BuildSize) error {
	if err := buildNamespaceReady(ctx, r.client, r.options.Namespace); err != nil {
		return err
	}
	// The Dockerfile step is looser than a site build's, which is sound only inside gVisor.
	if err := sandboxRuntimeReady(ctx, r.client, r.options.RuntimeClass); err != nil {
		return err
	}
	if err := r.client.applyNetworkPolicy(ctx, renderAppBuildEgressPolicy(r.options)); err != nil {
		return fmt.Errorf("app build egress policy: %w", err)
	}
	if err := applyBuildQuota(ctx, r.client, renderAppBuildQuota(r.options, largest)); err != nil {
		return err
	}
	scripts, err := renderAppBuildScripts(r.options.Namespace)
	if err != nil {
		return err
	}
	return applyBuildConfigMap(ctx, r.client, scripts)
}

func appStepFailureKind(step string, code int32) sitebuilds.FailureKind {
	switch step {
	case siteBuildFence:
		return sitebuilds.FailFence
	case siteBuildClone:
		return sitebuilds.FailClone
	case appBuildBuildKit, appBuildAnalyze:
		return checkoutFailure(step, code)
	case appBuildExport:
		// The export reads layers the app's code wrote, so its failures are the build's.
		return sitebuilds.FailBuild
	case appBuildBuildpacks:
		if code == exitNoBuildpack {
			return appbuilds.FailNoBuildpack
		}
		return sitebuilds.FailBuild
	case siteBuildCollect:
		if code == exitNoOutput {
			return sitebuilds.FailNoOutput
		}
	}
	return sitebuilds.FailPlatform
}

// checkoutFailure names the path checks the Dockerfile and analyze steps run before the build.
func checkoutFailure(step string, code int32) sitebuilds.FailureKind {
	switch {
	case code == exitOutsideCheckout:
		return sitebuilds.FailRootDir
	case code == exitNoDockerfile && step == appBuildBuildKit:
		return appbuilds.FailNoDockerfile
	case step == appBuildBuildKit:
		return sitebuilds.FailBuild
	}
	return sitebuilds.FailPlatform
}
