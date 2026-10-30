package main

import (
	"context"
	"errors"
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

// appBuildsBlocked fails the boot where app builds should run beside site builds in a cluster but the
// registry cannot issue. Site git builds off or single-host only say app builds are not offered.
func appBuildsBlocked(cfg config.AppConfig, internal *appregistry.Internal) error {
	if !appBuildsFlagOn(cfg) || siteBuildMode(cfg) != sitebuilds.ModeOn || internal.CanIssue() {
		return nil
	}
	return errors.New("appgitbuilds is on beside site git builds, but the internal registry does not issue: set APP_REGISTRY_HOST, its token key and the vault, or turn the flag off")
}

func appBuildsFlagOn(cfg config.AppConfig) bool {
	return features.Enabled(context.Background(), cfg.Features(), features.AppGitBuilds)
}

// enableAppBuilds wires app builds into the shared queue; call it before the dispatcher runs or the API serves.
func enableAppBuilds(cfg config.AppConfig, client *k8s.Client, files gitprovider.FileChecker,
	builds *sitebuilds.Service, dispatcher *sitebuilds.Dispatcher, args appBuildArgs) error {
	if args.registry.client == nil || args.registry.sweeper == nil {
		return errors.New("the internal registry's client and sweeper are not built; app builds have nowhere to push")
	}
	if cfg.AppBuild.SpoolPath == "" {
		return errors.New("APP_BUILD_SPOOL_PATH is required: a volume of its own where a publish takes in an image")
	}
	if err := verifyAppBuildCluster(cfg, client); err != nil {
		return err
	}
	spool, err := appBuildSpool(cfg.AppBuild.SpoolPath)
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

// appBuildSpool makes the spool a plain 0700 directory and clears what a crashed publish left there long ago.
func appBuildSpool(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("the app build spool %q must be an absolute path", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("make the app build spool: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("read the app build spool: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("the app build spool %s must be a directory, not a link or a file", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("restrict the app build spool: %w", err)
	}
	sweepSpool(dir)
	return dir, nil
}

func sweepSpool(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("WARN: app build spool: read %s: %v", dir, err)
		return
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
}
