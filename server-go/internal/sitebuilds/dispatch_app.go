package sitebuilds

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

const noPublisherReason = "this install has no image publisher wired, so nothing could be deployed; nothing was built"

// errAppsUnwired is an app build claimed by a replica with no app runner, which a wired replica's flag check prevents.
var errAppsUnwired = errors.New("app builds are not wired on this replica; build again")

// appKind builds an app's image and hands it to the publisher, which pushes and deploys it.
type appKind struct {
	execution *execution
	runner    AppRunner
	publisher AppPublisher
	// builder is the concrete builder, auto resolved against the commit.
	builder string
}

func (k *appKind) jobs() JobControl { return k.runner }

// connection is read before the build starts and again before it deploys; the flag going off stops either.
func (k *appKind) connection(ctx context.Context) (connected, error) {
	build, service := k.execution.build, k.execution.dispatcher.service
	if !service.appsOn(ctx) {
		return nil, ErrAppBuildsOff
	}
	connection, err := service.store.GetAppConnection(ctx, build.ProjectID, build.AppID)
	if err != nil || connection == nil {
		return nil, err
	}
	return connection, nil
}

func (k *appKind) preflight(ctx context.Context) *outcome {
	if k.publisher == nil {
		return refuse(FailPlatform, noPublisherReason)
	}
	if k.execution.build.App == nil {
		return refuse(FailPlatform, "the build has no app settings; build again")
	}
	builder, err := k.resolveBuilder(ctx)
	if err != nil {
		return refusal(err)
	}
	k.builder = builder
	return nil
}

// resolveBuilder answers dockerfile when the commit has the Dockerfile, buildpacks otherwise; it never guesses.
func (k *appKind) resolveBuilder(ctx context.Context) (string, error) {
	service, build := k.execution.dispatcher.service, k.execution.build
	settings := build.App
	if settings.Builder != AppBuilderAuto {
		return settings.Builder, nil
	}
	if service.apps == nil || service.apps.Files == nil {
		return "", errors.New("no reader of the commit's files is wired, so builder auto cannot be resolved")
	}
	dockerfile := path.Join(settings.RootDir, cmp.Or(settings.DockerfilePath, "Dockerfile"))
	repository := gitprovider.Repository{ID: build.RepositoryID, FullName: build.Repository}
	found, err := service.apps.Files.FileExists(ctx, build.InstallationID, repository, build.CommitSHA, dockerfile)
	if errors.Is(err, gitprovider.ErrNotFound) {
		return "", &InputError{Reason: "the GitHub App no longer covers " + build.Repository}
	}
	if err != nil {
		return "", fmt.Errorf("look for %s in the commit: %w", dockerfile, err)
	}
	if found {
		return AppBuilderDockerfile, nil
	}
	return AppBuilderBuildpacks, nil
}

func (k *appKind) failureMessage(status RunStatus) string {
	return k.runner.FailureMessage(status, k.execution.build)
}

// start passes build-visible secrets apart from the plain build arguments; both are hidden from the logs.
func (k *appKind) start(ctx context.Context, credential gitprovider.CloneCredential) error {
	run := k.execution
	service, build := run.dispatcher.service, run.build
	secrets, err := run.visibleSecrets(ctx)
	if err != nil {
		return err
	}
	run.hide("build argument ", build.App.BuildArgs)
	largest, err := service.apps.Limits.LargestAppBuild(ctx)
	if err != nil {
		return fmt.Errorf("size the build quota: %w", err)
	}
	repository := gitprovider.Repository{ID: build.RepositoryID, FullName: build.Repository}
	return k.runner.StartApp(ctx, AppRun{Build: build, RunID: run.runID, Builder: k.builder, CloneURL: service.provider.CloneURL(repository),
		CloneToken: credential.Token, Secrets: secrets, LargestPlan: largest})
}

// deliver hands the finished build to the publisher; a failure it names keeps its kind.
func (k *appKind) deliver(ctx context.Context) outcome {
	build := k.execution.build
	deployID, err := k.publisher.PublishBuild(ctx, AppPublish{BuildID: build.ID, RunID: k.execution.runID, ProjectID: build.ProjectID, AppID: build.AppID,
		CommitSHA: build.CommitSHA, MaxImageBytes: build.App.MaxImageBytes})
	k.execution.refreshLog(ctx)
	var failure *BuildFailure
	switch {
	case err == nil:
		return outcome{status: StatusSucceeded, deployID: deployID}
	case errors.As(err, &failure):
		return failedWith(failure.Kind, failure.Error())
	}
	return failedWith(FailDeploy, userMessage(err))
}

// unwiredApps stands in for an app build on a replica with no app runner: it stops before any job exists.
type unwiredApps struct{}

func (unwiredApps) jobs() JobControl { return nil }

func (unwiredApps) connection(context.Context) (connected, error) { return nil, errAppsUnwired }

func (unwiredApps) preflight(context.Context) *outcome {
	return refuse(FailPlatform, errAppsUnwired.Error())
}

func (unwiredApps) start(context.Context, gitprovider.CloneCredential) error { return errAppsUnwired }

func (unwiredApps) failureMessage(RunStatus) string { return errAppsUnwired.Error() }

func (unwiredApps) deliver(context.Context) outcome {
	return failedWith(FailPlatform, errAppsUnwired.Error())
}
