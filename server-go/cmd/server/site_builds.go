package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/docker/docker/client"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/dockerapps"
	"github.com/excalibase/provisioning-poc/internal/dockerbuilds"
	"github.com/excalibase/provisioning-poc/internal/gitprovider/githubapp"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

const singleHostBuildStartTimeout = 10 * time.Minute

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
	// docker is the engine on a single host (through the engine proxy); nil in a cluster.
	docker provisioner.DockerClient
}

// rawEngine is a Docker client that hands out the SDK client the runners call.
type rawEngine interface{ RawClient() *client.Client }

// siteBuildMode: off unless switched on where Sites is offered; single-host builds
// only behind their own flag (EXC-671), and is named, never silently off, without it.
func siteBuildMode(cfg config.AppConfig) sitebuilds.Mode {
	switch {
	case !cfg.SiteGitBuildsOn():
		return sitebuilds.ModeOff
	case cfg.ProvisionerMode == "docker" && !cfg.SingleHostSiteBuilds():
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
	runner, err := siteBuildRunner(args)
	if err != nil {
		log.Fatalf("site builds: %v", err)
	}
	if runner == nil {
		return off
	}
	provider, err := githubapp.New(githubapp.Config{
		AppID: cfg.SiteGit.AppID, AppSlug: cfg.SiteGit.AppSlug, ClientID: cfg.SiteGit.ClientID, ClientSecret: cfg.SiteGit.ClientSecret,
		PrivateKeyPEM: cfg.SiteGit.PrivateKeyPEM, WebhookSecret: cfg.SiteGit.WebhookSecret, APIURL: cfg.SiteGit.APIURL, WebURL: cfg.SiteGit.WebURL,
	})
	if err != nil {
		log.Fatalf("site builds: %v", err)
	}
	service := sitebuilds.NewService(sitebuilds.NewPostgresStore(args.sqlStore.DB()), provider, args.sites, args.limits,
		sitebuilds.NewVaultSecrets(args.vault), sitebuilds.Options{
			Mode: mode, NodeImage: cfg.SiteGit.NodeImage, Members: args.members,
			CallbackURL: siteGitCallbackURL(cfg),
		})
	dispatcher := sitebuilds.NewDispatcher(service, runner, siteBuildDispatcherOptions(cfg))
	return siteBuilds{handler: handler.NewSiteGitHandler(service, cfg.StudioURL), dispatcher: dispatcher, wired: true}
}

// siteBuildDispatcherOptions: a single host pulls the Node image inside Start, so its start window covers a first pull.
func siteBuildDispatcherOptions(cfg config.AppConfig) sitebuilds.DispatcherOptions {
	options := sitebuilds.DispatcherOptions{Slots: cfg.SiteGit.BuildSlots}
	if cfg.SingleHostSiteBuilds() {
		options.StartTimeout = singleHostBuildStartTimeout
	}
	return options
}

// siteBuildRunner is the single host's runner (EXC-671) or the cluster's; nil when this install has neither.
func siteBuildRunner(args siteBuildArgs) (sitebuilds.Runner, error) {
	cfg := args.cfg
	if cfg.ProvisionerMode == "docker" {
		engine, _ := args.docker.(rawEngine)
		return singleHostBuildRunner(context.Background(), cfg, engine)
	}
	kube, ok := args.kube.(*k8s.Client)
	if !ok || kube == nil {
		return nil, nil
	}
	if err := verifySiteBuildCluster(cfg, kube); err != nil {
		return nil, err
	}
	log.Printf("INFO: site git builds on: %d slot(s) in namespace %s under RuntimeClass %s",
		cfg.SiteGit.BuildSlots, cfg.SiteGit.BuildNamespace, cfg.AppRuntimeClass)
	return k8s.NewSiteBuildRunner(kube, k8s.SiteBuildOptions{
		Namespace: cfg.SiteGit.BuildNamespace, RuntimeClass: cfg.AppRuntimeClass, PriorityClass: cfg.SiteGit.PriorityClass, Slots: cfg.SiteGit.BuildSlots, PrepareImage: cfg.SiteGit.PrepareImage,
		ExtraDenyCIDRs: cfg.AppEgressExtraDenyCIDRs,
	}), nil
}

// singleHostBuildRunner runs builds through the engine proxy under the apps' sandbox runtime,
// and refuses to start on an engine without it, as a cluster without the RuntimeClass does.
func singleHostBuildRunner(ctx context.Context, cfg config.AppConfig, engine rawEngine) (*dockerbuilds.Runner, error) {
	if engine == nil || engine.RawClient() == nil {
		return nil, errors.New("git builds on a single host need the engine the databases run on")
	}
	sdk := engine.RawClient()
	apps, git := cfg.SingleHostApps, cfg.SiteGit
	ctx, cancel := context.WithTimeout(ctx, siteBuildCheckTimeout)
	defer cancel()
	// Podman is known before Verify, whose isolation check must make the network the way builds will.
	podman, err := dockerapps.IsPodman(ctx, sdk)
	if err != nil {
		return nil, err
	}
	runner, err := dockerbuilds.NewRunner(sdk, dockerbuilds.Options{
		NetworkPrefix: apps.NetworkPrefix, VolumePrefix: apps.VolumePrefix, SandboxRuntime: apps.SandboxRuntime,
		EgressImage: git.EgressImage, PrepareImage: git.PrepareImage,
		ExtraDenyCIDRs: cfg.AppEgressExtraDenyCIDRs, EgressAllow: git.EgressAllow, Isolate: podman,
	})
	if err != nil {
		return nil, err
	}
	if err := runner.Verify(ctx); err != nil {
		return nil, err
	}
	if apps.SandboxRuntime == "" {
		log.Printf("WARN: APP_SANDBOX_RUNTIME=none: site builds run without a sandbox, sharing the host's kernel directly")
	}
	if podman || !runner.HostIsolated() {
		log.Printf("WARN: this engine may give build networks an address on the host (Docker before 28, or Podman): " +
			"builds can reach host services listening on every address unless the host firewall drops traffic from container bridges")
	}
	log.Printf("INFO: site git builds on this single host: %d slot(s), under runtime %q, out only through the build egress proxy",
		git.BuildSlots, apps.SandboxRuntime)
	return runner, nil
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
