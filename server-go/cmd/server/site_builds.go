package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
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
	off := siteBuilds{handler: handler.NewSiteGitHandler(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: mode}), cfg.StudioURL)}
	if mode != sitebuilds.ModeOn {
		off.wired = mode == sitebuilds.ModeSingleHost
		if mode == sitebuilds.ModeSingleHost {
			log.Printf("INFO: site git builds are switched on but not offered on single-host; the API says so")
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
			CallbackURL: siteGitCallbackURL(cfg), Previews: previewSites(args.sites),
		})
	runner := k8s.NewSiteBuildRunner(client, k8s.SiteBuildOptions{
		Namespace: cfg.SiteGit.BuildNamespace, RuntimeClass: cfg.AppRuntimeClass, PriorityClass: cfg.SiteGit.PriorityClass, Slots: cfg.SiteGit.BuildSlots, PrepareImage: cfg.SiteGit.PrepareImage,
		ExtraDenyCIDRs: cfg.AppEgressExtraDenyCIDRs,
	})
	dispatcher := sitebuilds.NewDispatcher(service, runner, sitebuilds.DispatcherOptions{Slots: cfg.SiteGit.BuildSlots})
	log.Printf("INFO: site git builds on: %d slot(s) in namespace %s under RuntimeClass %s",
		cfg.SiteGit.BuildSlots, cfg.SiteGit.BuildNamespace, cfg.AppRuntimeClass)
	gitHandler := handler.NewSiteGitHandler(service, cfg.StudioURL)
	gitHandler.SetAuditLog(args.sqlStore)
	return siteBuilds{handler: gitHandler, dispatcher: dispatcher, wired: true}
}

// previewSites hands previews to git builds wherever Sites keeps them (EXC-660); while the flag is
// off git builds make none but still remove the held ones on close, delete and disconnect.
func previewSites(service *sites.Service) sitebuilds.PreviewSites {
	if service == nil || !service.KeepsPreviews() {
		return nil
	}
	return service
}

// siteGitCallbackURL is on Studio's origin: Studio calls /api there, so that is where the sign-in's browser cookie lives.
func siteGitCallbackURL(cfg config.AppConfig) string {
	return strings.TrimRight(cfg.StudioURL, "/") + "/api/sites/github/callback"
}

// verifySiteBuildCluster refuses to start without the build namespace the chart makes and the sandbox RuntimeClass.
func verifySiteBuildCluster(cfg config.AppConfig, client *k8s.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), siteBuildCheckTimeout)
	defer cancel()
	exists, err := client.NamespaceExists(ctx, cfg.SiteGit.BuildNamespace)
	if err != nil {
		return fmt.Errorf("check the build namespace %s: %w", cfg.SiteGit.BuildNamespace, err)
	}
	if !exists {
		return fmt.Errorf("the build namespace %s (SITES_BUILD_NAMESPACE) does not exist; the chart creates it and the builder's Role with features.sitesGitBuilds", cfg.SiteGit.BuildNamespace)
	}
	found, err := client.RuntimeClassExists(ctx, cfg.AppRuntimeClass)
	if err != nil {
		return fmt.Errorf("check RuntimeClass %q: %w", cfg.AppRuntimeClass, err)
	}
	if !found {
		return fmt.Errorf("RuntimeClass %q (APP_RUNTIME_CLASS) does not exist; builds run only under the sandbox", cfg.AppRuntimeClass)
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
