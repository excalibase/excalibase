package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const (
	// appBuildSpoolDir holds each publish's image on STORAGE_PATH, which is sized for it, unlike the pod's /tmp.
	appBuildSpoolDir = "app-build-spool"
	// appBuildSpoolMaxAge is well past any build's deadline: older entries are a crashed publish's leftovers.
	appBuildSpoolMaxAge = 6 * time.Hour
)

// appBuildArgs are what git builds of apps (EXC-547) need beyond the site builds' own.
type appBuildArgs struct {
	internal *appregistry.Internal
	registry appRegistryParts
	deploys  *service.AppDeployService
	apps     apphost.Store
	limits   sitebuilds.AppLimitsSource
}

// appBuildsWanted: the flag on, a registry that issues, and site git builds running in a cluster.
func appBuildsWanted(cfg config.AppConfig, internal *appregistry.Internal) bool {
	return appBuildsFlagOn(cfg) && internal.CanIssue() && siteBuildMode(cfg) == sitebuilds.ModeOn
}

func appBuildsFlagOn(cfg config.AppConfig) bool {
	return features.Enabled(context.Background(), cfg.Features(), features.AppGitBuilds)
}

// enableAppBuilds wires app builds into the shared queue; call it before the dispatcher runs or the API serves.
func enableAppBuilds(cfg config.AppConfig, client *k8s.Client, files gitprovider.FileChecker,
	builds *sitebuilds.Service, dispatcher *sitebuilds.Dispatcher, args appBuildArgs) error {
	if err := verifyAppBuildCluster(cfg, client); err != nil {
		return err
	}
	spool, err := appBuildSpool(cfg.StoragePath)
	if err != nil {
		return err
	}
	runner := k8s.NewAppBuildRunner(client, appBuildOptions(cfg))
	publisher := service.NewAppBuildPublisher(args.internal, runner, args.registry.client, args.registry.sweeper, args.deploys, spool)
	builds.EnableApps(sitebuilds.AppSupport{Apps: args.apps, Limits: args.limits, Files: files, Flags: cfg.Features()})
	dispatcher.EnableApps(appbuilds.NewRunner(runner), service.NewAppBuildQueuePublisher(publisher, args.apps))
	log.Printf("INFO: app git builds on in namespace %s, sharing the site builds' %d slot(s)", cfg.AppBuild.Namespace, cfg.SiteGit.BuildSlots)
	return nil
}

// appBuildOptions share the site builds' slots, priority class and helper images; only the namespace and builders are the apps' own.
func appBuildOptions(cfg config.AppConfig) k8s.AppBuildOptions {
	return k8s.AppBuildOptions{
		Namespace: cfg.AppBuild.Namespace, RuntimeClass: cfg.AppRuntimeClass, PriorityClass: cfg.SiteGit.PriorityClass,
		Slots: cfg.SiteGit.BuildSlots, PrepareImage: cfg.SiteGit.PrepareImage,
		ToolImage:     sitebuilds.NodeImage(cfg.SiteGit.NodeImage, sitebuilds.DefaultNodeVersion),
		BuildKitImage: cfg.AppBuild.BuildKitImage, BuilderImage: cfg.AppBuild.BuilderImage, RunImage: cfg.AppBuild.RunImage,
		CraneImage: cfg.AppBuild.CraneImage, ExtraDenyCIDRs: cfg.AppEgressExtraDenyCIDRs,
	}
}

// verifyAppBuildCluster refuses to start app builds without their namespace and the sandbox RuntimeClass.
func verifyAppBuildCluster(cfg config.AppConfig, client *k8s.Client) error {
	return verifyBuildCluster(client, cfg.AppBuild.Namespace,
		"(APP_BUILD_NAMESPACE) does not exist; the chart creates it and the builder's Role with features.appGitBuilds", cfg.AppRuntimeClass)
}

// appBuildSpool makes the spool directory and clears what a crashed publish left there long ago.
func appBuildSpool(storagePath string) (string, error) {
	dir := filepath.Join(storagePath, appBuildSpoolDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("make the app build spool: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read the app build spool: %w", err)
	}
	cutoff := time.Now().Add(-appBuildSpoolMaxAge)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			log.Printf("WARN: app build spool: remove %s: %v", entry.Name(), err)
		}
	}
	return dir, nil
}
