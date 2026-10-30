package sitebuilds

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// siteKind builds a site's output folder and deploys it through the Sites upload path.
type siteKind struct {
	execution *execution
	runner    Runner
}

func (k *siteKind) jobs() JobControl { return k.runner }

func (k *siteKind) connection(ctx context.Context) (connected, error) {
	build := k.execution.build
	connection, err := k.execution.dispatcher.service.store.GetConnection(ctx, build.ProjectID, build.SiteID)
	if err != nil || connection == nil {
		return nil, err
	}
	return connection, nil
}

func (k *siteKind) preflight(context.Context) *outcome { return nil }

func (k *siteKind) failureMessage(status RunStatus) string {
	return failureMessage(status, k.execution.build.Limits, k.execution.build.Settings)
}

// start gathers the build's variables and starts its Job.
func (k *siteKind) start(ctx context.Context, credential gitprovider.CloneCredential) error {
	service, build := k.execution.dispatcher.service, k.execution.build
	env, err := k.environment(ctx)
	if err != nil {
		return err
	}
	largest, err := service.limits.LargestBuild(ctx)
	if err != nil {
		return fmt.Errorf("size the build quota: %w", err)
	}
	repository := gitprovider.Repository{ID: build.RepositoryID, FullName: build.Repository}
	settings, limits := build.Settings, build.Limits
	return k.runner.Start(ctx, RunSpec{
		BuildID: k.execution.runID, SiteID: build.SiteID, ProjectID: build.ProjectID,
		Image: NodeImage(service.options.NodeImage, settings.NodeVersion), CloneURL: service.provider.CloneURL(repository),
		Commit: build.CommitSHA, CloneToken: credential.Token, RootDir: settings.RootDir, OutputDir: settings.OutputDir,
		InstallCommand: settings.InstallCommand, BuildCommand: settings.BuildCommand,
		CPUMillis: limits.CPUMillis, Memory: limits.Memory, Disk: limits.Disk, Timeout: time.Duration(limits.TimeoutSeconds) * time.Second,
		Env: env, LargestPlan: largest,
	})
}

// environment is the site's declared variables plus the project secrets marked build-visible.
func (k *siteKind) environment(ctx context.Context) (map[string]string, error) {
	declared := k.execution.build.Settings.Env
	env := make(map[string]string, len(declared))
	for name, value := range declared {
		env[name] = value
	}
	secrets, err := k.execution.visibleSecrets(ctx)
	if err != nil {
		return nil, err
	}
	for name, value := range secrets {
		env[name] = value
	}
	return env, nil
}

// deliver collects the packed output and hands it to the Sites deploy path, every upload check included.
func (k *siteKind) deliver(ctx context.Context) outcome {
	run := k.execution
	service, build := run.dispatcher.service, run.build
	ceiling, err := service.sites.MaxUploadBytes(ctx, build.ProjectID)
	if err != nil {
		return failedWith(FailPlatform, userMessage(err))
	}
	spool, err := os.CreateTemp(run.dispatcher.options.SpoolDir, "site-build-*.tar.gz")
	if err != nil {
		return failedWith(FailPlatform, userMessage(err))
	}
	defer func() {
		_ = spool.Close()
		if err := os.Remove(spool.Name()); err != nil {
			log.Printf("site build %s: remove spool: %v", build.ID, err)
		}
	}()
	if err := k.runner.Collect(ctx, run.runID, spool, ceiling); err != nil {
		return failedWith(FailDeploy, userMessage(err))
	}
	run.refreshLog(ctx)
	deploy, err := k.deployWithRetry(ctx, spool)
	if err != nil {
		return failedWith(FailDeploy, userMessage(err))
	}
	return outcome{status: StatusSucceeded, deployID: deploy.ID}
}

// busy are refusals that clear once another deploy of the project or site finishes.
func busy(err error) bool {
	return errors.Is(err, sites.ErrProjectDeployRunning) || errors.Is(err, sites.ErrTooManyDeploys) || errors.Is(err, sites.ErrSiteBusy)
}

func (k *siteKind) deployWithRetry(ctx context.Context, spool *os.File) (*sites.Deploy, error) {
	service, build, options := k.execution.dispatcher.service, k.execution.build, k.execution.dispatcher.options
	var lastErr error
	for try := 0; try < options.DeployTries; try++ {
		if _, err := spool.Seek(0, 0); err != nil {
			return nil, err
		}
		deploy, err := service.sites.Deploy(ctx, sites.DeployRequest{
			ProjectID: build.ProjectID, SiteID: build.SiteID, Actor: build.RequestedBy, Label: build.DeployLabel(), Body: spool, SlotTaken: true,
		})
		if !busy(err) {
			return deploy, err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(options.DeployRetry):
		}
	}
	return nil, lastErr
}
