package sites_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

func newServerHarness(t *testing.T, on bool) *serviceHarness {
	t.Helper()
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins()
	flags := features.NewStatic()
	if on {
		flags = features.NewStatic(features.SitesServer)
	}
	service, err := sites.NewService(store, publisher, origins, serverOptions(t, flags, store))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &serviceHarness{service: service, store: store, publisher: publisher, origins: origins}
}

func serverOptions(t *testing.T, flags features.Flags, code sites.CodeStore) sites.Options {
	return sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(), Flags: flags, Code: code, OutboundWorker: "excalibase-sites-outbound",
		Outbound: &sitestest.Outbound{Worker: "excalibase-sites-outbound"}}
}

func (h *serviceHarness) createServerSite(t *testing.T, name string) *sites.Site {
	t.Helper()
	site, err := h.service.CreateKind(context.Background(), testProject, name, sites.KindServer, nil, testActor)
	if err != nil {
		t.Fatalf("CreateKind: %v", err)
	}
	return site
}

func TestServerSitesNeedTheOutboundWorker(t *testing.T) {
	options := serverOptions(t, features.NewStatic(features.SitesServer), sitestest.NewMemoryStore())
	options.OutboundWorker = ""
	service, err := sites.NewService(sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins(), options)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := service.CreateKind(context.Background(), testProject, "ssr", sites.KindServer, nil, testActor); !errors.Is(err, sites.ErrServerSitesOff) {
		t.Fatalf("err = %v; server code must not run before its egress is filtered", err)
	}
}

func TestAnUnknownKindIsUnmountedWhileOff(t *testing.T) {
	off := newServerHarness(t, false)
	if _, err := off.service.CreateKind(context.Background(), testProject, "odd", "edge", nil, testActor); !errors.Is(err, sites.ErrServerSitesOff) {
		t.Fatalf("err = %v, want the feature's 404", err)
	}
}

func TestServerSitesAreOffUntilTheirFeatureIs(t *testing.T) {
	off := newServerHarness(t, false)
	if _, err := off.service.CreateKind(context.Background(), testProject, "ssr", sites.KindServer, nil, testActor); !errors.Is(err, sites.ErrServerSitesOff) {
		t.Fatalf("err = %v, want server sites off", err)
	}
	on := newServerHarness(t, true)
	if site := on.createServerSite(t, "ssr"); site.Kind != sites.KindServer {
		t.Fatalf("site = %+v", site)
	}
	if static := on.createSite(t, "plain"); static.Kind != sites.KindStatic {
		t.Fatalf("a site with no kind is %q, want static", static.Kind)
	}
	if _, err := on.service.CreateKind(context.Background(), testProject, "odd", "edge", nil, testActor); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
}

func TestServerSitesHaveTheirOwnLimit(t *testing.T) {
	h := newServerHarness(t, true)
	h.createServerSite(t, "ssr-one")
	_, err := h.service.CreateKind(context.Background(), testProject, "ssr-two", sites.KindServer, nil, testActor)
	if !errors.Is(err, sites.ErrServerSiteLimitReached) {
		t.Fatalf("err = %v, want the server site limit", err)
	}
	h.createSite(t, "still-static")
}

func TestServerDeployPublishesModulesAndKeepsThemOutOfTheAssets(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	deploy, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1"))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if deploy.Kind != sites.KindServer || deploy.Server == nil || deploy.Server.Main != "worker.js" || deploy.Server.Modules != 2 ||
		deploy.Server.CodeBytes != int64(len("export default 1")+len("export const page = 1")) || deploy.FileCount != 2 {
		t.Fatalf("deploy = %+v server = %+v", deploy, deploy.Server)
	}
	version := h.publisher.Versions[sites.ScriptName(site)]
	if version.MainModule != "worker.js" || version.Assets.NotFoundHandling != cfworkers.NotFoundNone ||
		version.CompatibilityDate != "2025-09-01" || strings.Join(version.CompatibilityFlags, ",") != "nodejs_compat,global_fetch_strictly_public" {
		t.Fatalf("version = %+v", version)
	}
	// The plan's limits travel with the script too, behind the router's per-call ones.
	if version.Limits == nil || version.Limits.CPUMillis != sites.DefaultLimits.MaxServerCPUMillis || version.Limits.Subrequests != sites.DefaultLimits.MaxServerSubrequests {
		t.Errorf("limits = %+v", version.Limits)
	}
	if len(version.Bindings) != 0 {
		t.Errorf("bindings = %v; v1 binds only ASSETS", version.Bindings)
	}
	if body := h.publisher.ModuleBodies[sites.ScriptName(site)]["worker.js"]; body != "export default 1" {
		t.Errorf("worker.js = %q", body)
	}
	for _, stored := range h.publisher.Stored {
		if stored == "export default 1" {
			t.Fatal("server code was uploaded as a public asset")
		}
	}
	if len(h.store.Code) != 1 {
		t.Fatalf("code kept for %d deploys, want 1", len(h.store.Code))
	}
}

func TestAStaticBuildDoesNotDeployToAServerSite(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	_, err := h.deploy(t, site, viteDist(t))
	wantArchiveError(t, err, "site.json")
}

func TestAStaticSiteStillPublishesAssetsOnlyWithTheFeatureOn(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createSite(t, "plain")
	deploy, err := h.deploy(t, site, viteDist(t))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	version := h.publisher.Versions[sites.ScriptName(site)]
	if deploy.Kind != sites.KindStatic || deploy.Server != nil || version.MainModule != "" || len(version.Modules) != 0 {
		t.Fatalf("deploy = %+v version = %+v", deploy, version)
	}
	if len(h.store.Code) != 0 {
		t.Fatal("a static deploy kept code")
	}
}

func TestRollbackRepublishesTheEarlierDeploysCode(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	first, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'v1'"))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'v2'")); err != nil {
		t.Fatalf("second: %v", err)
	}
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, first.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	script := sites.ScriptName(site)
	if body := h.publisher.ModuleBodies[script]["worker.js"]; body != "export default 'v1'" {
		t.Fatalf("worker.js after rollback = %q", body)
	}
	if version := h.publisher.Versions[script]; version.MainModule != "worker.js" || version.Assets.NotFoundHandling != cfworkers.NotFoundNone {
		t.Fatalf("version = %+v", version)
	}
}

func TestCodeIsKeptForTheRollbackWindowOnly(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	var deploys []*sites.Deploy
	for i := range sites.ServerCodeKept + 2 {
		deploy, err := h.deploy(t, site, sitestest.ServerDist(t, "export default "+string(rune('a'+i))))
		if err != nil {
			t.Fatalf("deploy %d: %v", i, err)
		}
		deploys = append(deploys, deploy)
	}
	if len(h.store.Code) != sites.ServerCodeKept {
		t.Fatalf("code kept for %d deploys, want %d", len(h.store.Code), sites.ServerCodeKept)
	}
	if !h.store.PrunedWithBudget(site.ID, sites.ServerCodeBudget(sites.DefaultLimits)) {
		t.Error("pruned without the site's byte budget")
	}
	_, err := h.service.Rollback(context.Background(), testProject, site.ID, deploys[0].ID)
	if !errors.Is(err, sites.ErrServerCodeGone) {
		t.Fatalf("err = %v, want the code gone", err)
	}
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, deploys[2].ID); err != nil {
		t.Fatalf("rollback inside the window: %v", err)
	}
}

func TestTheLiveDeploysCodeIsNeverPruned(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	first, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'live'"))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	h.publisher.FailPut = errors.New("cloudflare answered 500")
	for range sites.ServerCodeKept + 1 {
		if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'broken'")); err == nil {
			t.Fatal("want the publish to fail")
		}
	}
	if _, ok := h.store.Code[first.ID]; !ok || len(h.store.Code) != 1 {
		t.Fatalf("code kept = %d, want only the live deploy's", len(h.store.Code))
	}
}

func TestServerDeploysStopWhenTheFeatureIsSwitchedOff(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	first, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1"))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	off, err := sites.NewService(h.store, h.publisher, h.origins, serverOptions(t, features.NewStatic(), h.store))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h.service = off
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 2")); !errors.Is(err, sites.ErrServerSitesOff) {
		t.Fatalf("deploy err = %v, want server sites off", err)
	}
	if _, err := off.Rollback(context.Background(), testProject, site.ID, first.ID); !errors.Is(err, sites.ErrServerSitesOff) {
		t.Fatalf("rollback err = %v, want server sites off", err)
	}
}

func TestTheUploadCeilingGrowsOnlyForServerSites(t *testing.T) {
	off, on := newServerHarness(t, false), newServerHarness(t, true)
	ceilingOff, _ := off.service.MaxUploadBytes(context.Background(), testProject)
	ceilingOn, _ := on.service.MaxUploadBytes(context.Background(), testProject)
	if ceilingOff != sites.MaxArchiveBytes(sites.DefaultLimits) || ceilingOn != sites.MaxServerArchiveBytes(sites.DefaultLimits) {
		t.Fatalf("ceilings = %d off, %d on", ceilingOff, ceilingOn)
	}
}

type failingCode struct{ *sitestest.MemoryStore }

func (failingCode) PutCode(context.Context, *sites.Deploy, []byte) error {
	return errors.New("pq: connection to 10.0.0.5 refused")
}

// Nothing is published without its code kept, and the deploy's reason names no internals.
func TestAServerDeployWhoseCodeCannotBeKeptPublishesNothing(t *testing.T) {
	store, publisher := sitestest.NewMemoryStore(), sitestest.NewPublisher()
	service, err := sites.NewService(store, publisher, sitestest.NewOrigins(), serverOptions(t, features.NewStatic(features.SitesServer), failingCode{store}))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := &serviceHarness{service: service, store: store, publisher: publisher}
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); !errors.Is(err, sites.ErrCodeNotKept) {
		t.Fatalf("err = %v, want the code not kept", err)
	}
	if len(publisher.Versions) != 0 {
		t.Fatal("published a deploy whose code is not kept")
	}
	deploys, _ := service.ListDeploys(context.Background(), testProject, site.ID)
	if len(deploys) != 1 || deploys[0].Status != sites.DeployFailed || strings.Contains(deploys[0].FailureReason, "10.0.0.5") {
		t.Fatalf("deploys = %+v", deploys)
	}
}

func TestAServerRollbackLoadsNoCodeBeforeItsSlot(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	first, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1"))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := h.store.ClaimDeploy(context.Background(), testProject, site.ID, "other-holder", h.store.Now().Add(sites.MaxDeployDuration)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	reads := h.store.CodeReads
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, first.ID); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("err = %v, want busy", err)
	}
	if h.store.CodeReads != reads {
		t.Fatal("a refused rollback read the kept code")
	}
}

func TestServerRollbacksCountAgainstTheHourlyDeploys(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	first, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1"))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	before := len(h.store.Attempts[testProject])
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, first.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(h.store.Attempts[testProject]) != before+1 {
		t.Fatalf("attempts %d → %d; a server rollback re-uploads all its code", before, len(h.store.Attempts[testProject]))
	}
}

func TestSwitchingTheFeatureOffStopsServingServerSites(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	static := h.createSite(t, "plain")
	for _, target := range []*sites.Site{site, static} {
		archive := viteDist(t)
		if target.Kind == sites.KindServer {
			archive = sitestest.ServerDist(t, "export default 1")
		}
		if _, err := h.deploy(t, target, archive); err != nil {
			t.Fatalf("Deploy %s: %v", target.Name, err)
		}
	}
	off, err := sites.NewService(h.store, h.publisher, h.origins, serverOptions(t, features.NewStatic(), h.store))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := off.ReconcileServerSites(context.Background()); err != nil {
		t.Fatalf("Reconcile off: %v", err)
	}
	if _, routed := h.publisher.Hosts[site.Hostname]; routed {
		t.Fatal("a server site is still routed with its feature off")
	}
	if h.publisher.Hosts[static.Hostname] != sites.ScriptName(static) {
		t.Fatal("a static site lost its route")
	}
	// Nothing routes it back while the feature is off.
	if err := off.RouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("RouteProjectSites: %v", err)
	}
	if _, routed := h.publisher.Hosts[site.Hostname]; routed {
		t.Fatal("project routing served a server site with its feature off")
	}
	if err := h.service.ReconcileServerSites(context.Background()); err != nil {
		t.Fatalf("Reconcile on: %v", err)
	}
	if h.publisher.Hosts[site.Hostname] != sites.ScriptName(site) {
		t.Fatal("switching the feature on again did not serve the live deploy")
	}
}

// What Cloudflare runs keeps its code even when its deploy failed after the
// swap and a later deploy failed before one.
func TestTheCodeCloudflareRunsIsNeverPruned(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'old'")); err != nil {
		t.Fatalf("first: %v", err)
	}
	h.publisher.FailKV = errors.New("kv down")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'running'")); err == nil {
		t.Fatal("want the route write to fail")
	}
	h.publisher.FailKV = nil
	h.publisher.FailSession = errors.New("cloudflare answered 500")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'never'")); err == nil {
		t.Fatal("want the upload session to fail")
	}
	running := h.store.Published(site.ID)
	if running == "" {
		t.Fatal("no deploy is recorded as published")
	}
	if _, kept := h.store.Code[running]; !kept {
		t.Fatal("the code Cloudflare runs was pruned")
	}
}

func TestARefusedServerRollbackGivesItsSlotBack(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	first, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1"))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := h.store.ClaimDeploy(context.Background(), testProject, site.ID, "other-holder", h.store.Now().Add(sites.MaxDeployDuration)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	before := len(h.store.Attempts[testProject])
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, first.ID); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("err = %v, want busy", err)
	}
	if len(h.store.Attempts[testProject]) != before {
		t.Fatal("a rollback refused as busy kept its hourly slot")
	}
}

func TestAServerPublishNeverGoesOutWithoutLimits(t *testing.T) {
	store, publisher := sitestest.NewMemoryStore(), sitestest.NewPublisher()
	options := serverOptions(t, features.NewStatic(features.SitesServer), store)
	limits := sites.DefaultLimits
	limits.MaxServerCPUMillis = 0
	options.DeployLimits = fixedLimits{limits: limits}
	service, err := sites.NewService(store, publisher, sitestest.NewOrigins(), options)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := &serviceHarness{service: service, store: store, publisher: publisher}
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err == nil {
		t.Fatal("published server code with no CPU limit")
	}
	if len(publisher.Versions) != 0 {
		t.Fatal("a version without limits reached the CDN")
	}
}

func TestStaleServerDeploysStillRunningKeepNoCode(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	stale := &sites.Deploy{ID: "stale", SiteID: site.ID, ProjectID: testProject, Kind: sites.KindServer, Status: sites.DeployPending,
		CreatedAt: h.store.Now().Add(-2 * sites.MaxDeployDuration)}
	if err := h.store.CreateDeploy(context.Background(), stale); err != nil {
		t.Fatalf("CreateDeploy: %v", err)
	}
	if err := h.store.PutCode(context.Background(), stale, []byte("x")); err != nil {
		t.Fatalf("PutCode: %v", err)
	}
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if _, kept := h.store.Code["stale"]; kept {
		t.Fatal("code of a deploy that outlived its lease is kept forever")
	}
}

func TestTheOffSwitchTriesEverySiteAndReportsTheRest(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	off, err := sites.NewService(h.store, h.publisher, h.origins, serverOptions(t, features.NewStatic(), h.store))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h.publisher.FailKV = errors.New("kv down")
	if err := off.ReconcileServerSites(context.Background()); err == nil {
		t.Fatal("a failed unroute was not reported")
	}
}

// No server code reaches Cloudflare unless the router sends its requests
// through the configured outbound Worker (ADR 0045 decision 5).
func TestServerCodeIsPublishedOnlyBehindTheOutboundWorker(t *testing.T) {
	for name, binding := range map[string]*sitestest.Outbound{
		"another outbound": {Worker: "someone-elses"},
		"no outbound":      {},
		"unreadable":       {Fail: errors.New("cloudflare answered 403")},
	} {
		store, publisher := sitestest.NewMemoryStore(), sitestest.NewPublisher()
		options := serverOptions(t, features.NewStatic(features.SitesServer), store)
		options.Outbound = binding
		service, err := sites.NewService(store, publisher, sitestest.NewOrigins(), options)
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		h := &serviceHarness{service: service, store: store, publisher: publisher}
		site := h.createServerSite(t, "ssr")
		if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); !errors.Is(err, sites.ErrOutboundNotBound) {
			t.Errorf("%s: err = %v, want the outbound refusal", name, err)
		}
		if publisher.Sessions != 0 || len(publisher.Versions) != 0 {
			t.Errorf("%s: server code reached Cloudflare", name)
		}
	}
}

func TestTheOutboundBindingIsReadAgainAfterAWhile(t *testing.T) {
	h := newServerHarness(t, true)
	binding := &sitestest.Outbound{Worker: "excalibase-sites-outbound"}
	options := serverOptions(t, features.NewStatic(features.SitesServer), h.store)
	options.Outbound = binding
	service, err := sites.NewService(h.store, h.publisher, h.origins, options)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	clock := time.Now()
	service.SetNow(func() time.Time { return clock })
	h.service = service
	site := h.createServerSite(t, "ssr")
	for range 2 {
		if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
	}
	if binding.Reads != 1 {
		t.Fatalf("reads = %d, want one while it is fresh", binding.Reads)
	}
	clock = clock.Add(10 * time.Minute)
	binding.Worker = "replaced"
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 2")); !errors.Is(err, sites.ErrOutboundNotBound) {
		t.Fatalf("err = %v, want a changed binding refused", err)
	}
}

func TestStaticSitesNeverReadTheOutboundBinding(t *testing.T) {
	h := newServerHarness(t, true)
	binding := &sitestest.Outbound{Fail: errors.New("unreachable")}
	options := serverOptions(t, features.NewStatic(features.SitesServer), h.store)
	options.Outbound = binding
	service, err := sites.NewService(h.store, h.publisher, h.origins, options)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h.service = service
	site := h.createSite(t, "plain")
	if _, err := h.deploy(t, site, sitestest.Dist(t, "<h1>hi</h1>")); err != nil || binding.Reads != 0 {
		t.Fatalf("static deploy err=%v reads=%d", err, binding.Reads)
	}
}
