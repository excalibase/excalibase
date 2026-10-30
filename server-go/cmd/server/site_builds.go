package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/gitprovider/githubapp"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

const siteBuildCheckTimeout = 30 * time.Second

// accessLogRedactedParams never reach the access log: read-only SQL, and the git
// host's sign-in code and state on the callback (EXC-354).
var accessLogRedactedParams = []string{"sql", "code", "state"}

// siteGitWebhookRate bounds deliveries per address; GitHub sends from a shared pool, so it is generous.
var siteGitWebhookRate = custommw.RateLimit(custommw.PerIP, 600, time.Minute)

// siteBuilds is git builds of sites (EXC-354) as main wires them.
type siteBuilds struct {
	handler    *handler.SiteGitHandler
	dispatcher *sitebuilds.Dispatcher
	// wired is what the boot check asks: a cluster runner, or single-host, which refuses by name.
	wired bool
	// apps serves git builds of apps (EXC-547) over the same service; appsWired is whether they can run here.
	apps      *handler.AppGitHandler
	appsWired bool
}

type siteBuildArgs struct {
	cfg      config.AppConfig
	sqlStore storage.PlatformStore
	kube     k8s.KubeClient
	vault    vaultclient.VaultClient
	sites    *sites.Service
	limits   sitebuilds.LimitsSource
	// members judges a connector by the deploy route's own checks.
	members sitebuilds.MembershipChecker
	apps    appBuildArgs
}

// siteBuildMode: off unless switched on where Sites is offered; single-host is named, never silently off.
func siteBuildMode(cfg config.AppConfig) sitebuilds.Mode {
	switch {
	case !cfg.SiteGitBuildsOn():
		return sitebuilds.ModeOff
	case cfg.ProvisionerMode == "docker":
		return sitebuilds.ModeSingleHost
	}
	return sitebuilds.ModeOn
}

func newSiteBuilds(args siteBuildArgs) siteBuilds {
	cfg := args.cfg
	mode := siteBuildMode(cfg)
	// The off service keeps the store: an app's disconnect answers whatever the install offers.
	offService := sitebuilds.NewService(offStore(args.sqlStore), nil, nil, nil, nil, sitebuilds.Options{Mode: mode})
	off := siteBuilds{handler: handler.NewSiteGitHandler(offService, cfg.StudioURL), apps: handler.NewAppGitHandler(offService)}
	if mode != sitebuilds.ModeOn {
		off.wired = mode == sitebuilds.ModeSingleHost
		if mode == sitebuilds.ModeSingleHost {
			log.Printf("INFO: site git builds are switched on but not offered on single-host; the API says so")
		}
		if appBuildsFlagOn(cfg) {
			log.Printf("WARN: appgitbuilds is on but app git builds run only beside site git builds in a cluster (SITES_GIT_BUILDS_ENABLED)")
		}
		return off
	}
	client, ok := args.kube.(*k8s.Client)
	if !ok || client == nil {
		return off
	}
	provider, err := githubapp.New(githubapp.Config{
		AppID: cfg.SiteGit.AppID, AppSlug: cfg.SiteGit.AppSlug, ClientID: cfg.SiteGit.ClientID, ClientSecret: cfg.SiteGit.ClientSecret,
		PrivateKeyPEM: cfg.SiteGit.PrivateKeyPEM, WebhookSecret: cfg.SiteGit.WebhookSecret, APIURL: cfg.SiteGit.APIURL, WebURL: cfg.SiteGit.WebURL,
	})
	if err != nil {
		log.Fatalf("site builds: %v", err)
	}
	if err := verifySiteBuildCluster(cfg, client); err != nil {
		log.Fatalf("site builds: %v", err)
	}
	service := sitebuilds.NewService(sitebuilds.NewPostgresStore(args.sqlStore.DB()), provider, args.sites, args.limits,
		sitebuilds.NewVaultSecrets(args.vault), sitebuilds.Options{
			Mode: mode, NodeImage: cfg.SiteGit.NodeImage, Members: args.members,
			CallbackURL: siteGitCallbackURL(cfg),
		})
	runner := k8s.NewSiteBuildRunner(client, k8s.SiteBuildOptions{
		Namespace: cfg.SiteGit.BuildNamespace, RuntimeClass: cfg.AppRuntimeClass, PriorityClass: cfg.SiteGit.PriorityClass, Slots: cfg.SiteGit.BuildSlots, PrepareImage: cfg.SiteGit.PrepareImage,
		ExtraDenyCIDRs: cfg.AppEgressExtraDenyCIDRs,
	})
	dispatcher := sitebuilds.NewDispatcher(service, runner, sitebuilds.DispatcherOptions{Slots: cfg.SiteGit.BuildSlots})
	log.Printf("INFO: site git builds on: %d slot(s) in namespace %s under RuntimeClass %s",
		cfg.SiteGit.BuildSlots, cfg.SiteGit.BuildNamespace, cfg.AppRuntimeClass)
	appsWired := wireAppBuilds(cfg, client, provider, service, dispatcher, args.apps)
	return siteBuilds{handler: handler.NewSiteGitHandler(service, cfg.StudioURL), dispatcher: dispatcher, wired: true,
		apps: handler.NewAppGitHandler(service), appsWired: appsWired}
}

// wireAppBuilds turns on git builds of apps where they are wanted, and stops the boot where they are wanted but cannot run.
func wireAppBuilds(cfg config.AppConfig, client *k8s.Client, files gitprovider.FileChecker,
	service *sitebuilds.Service, dispatcher *sitebuilds.Dispatcher, args appBuildArgs) bool {
	if err := appBuildsBlocked(cfg, args.internal); err != nil {
		log.Fatalf("app builds: %v", err)
	}
	if !appBuildsWanted(cfg, args.internal) {
		return false
	}
	if err := enableAppBuilds(cfg, client, files, service, dispatcher, args); err != nil {
		log.Fatalf("app builds: %v", err)
	}
	return true
}

// offStore is the build store where there is a platform database; without one a disconnect answers unavailable.
func offStore(sqlStore storage.PlatformStore) sitebuilds.Store {
	if sqlStore == nil {
		return nil
	}
	return sitebuilds.NewPostgresStore(sqlStore.DB())
}

// siteGitCallbackURL is on Studio's origin: Studio calls /api there, so that is where the sign-in's browser cookie lives.
func siteGitCallbackURL(cfg config.AppConfig) string {
	return strings.TrimRight(cfg.StudioURL, "/") + "/api/sites/github/callback"
}

// verifySiteBuildCluster refuses to start without the build namespace the chart makes and the sandbox RuntimeClass.
func verifySiteBuildCluster(cfg config.AppConfig, client *k8s.Client) error {
	return verifyBuildCluster(client, cfg.SiteGit.BuildNamespace,
		"(SITES_BUILD_NAMESPACE) does not exist; the chart creates it and the builder's Role with features.sitesGitBuilds", cfg.AppRuntimeClass)
}

// verifyBuildCluster checks a build namespace and the sandbox RuntimeClass; missing says what is missing and who makes it.
func verifyBuildCluster(client *k8s.Client, namespace, missing, runtimeClass string) error {
	ctx, cancel := context.WithTimeout(context.Background(), siteBuildCheckTimeout)
	defer cancel()
	exists, err := client.NamespaceExists(ctx, namespace)
	if err != nil {
		return fmt.Errorf("check the build namespace %s: %w", namespace, err)
	}
	if !exists {
		return fmt.Errorf("the build namespace %s %s", namespace, missing)
	}
	found, err := client.RuntimeClassExists(ctx, runtimeClass)
	if err != nil {
		return fmt.Errorf("check RuntimeClass %q: %w", runtimeClass, err)
	}
	if !found {
		return fmt.Errorf("RuntimeClass %q (APP_RUNTIME_CLASS) does not exist; builds run only under the sandbox", runtimeClass)
	}
	return nil
}

// startSiteBuildDispatcher runs queued builds until the server stops; every replica runs one.
func startSiteBuildDispatcher(builds siteBuilds) func() {
	if builds.dispatcher == nil {
		return func() {
			// git builds off or single-host: nothing to dispatch
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		builds.dispatcher.Run(context.Background())
	}()
	return func() {
		builds.dispatcher.Shutdown()
		<-done
	}
}
