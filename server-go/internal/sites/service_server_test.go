package sites_test

import (
	"context"
	"errors"
	"strings"
	"testing"

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
	service, err := sites.NewService(store, publisher, origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(), Flags: flags, Code: store})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &serviceHarness{service: service, store: store, publisher: publisher, origins: origins}
}

func (h *serviceHarness) createServerSite(t *testing.T, name string) *sites.Site {
	t.Helper()
	site, err := h.service.CreateKind(context.Background(), testProject, name, sites.KindServer, nil, testActor)
	if err != nil {
		t.Fatalf("CreateKind: %v", err)
	}
	return site
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
		deploy.Server.CodeBytes != int64(len("export default 1")) || deploy.FileCount != 2 {
		t.Fatalf("deploy = %+v server = %+v", deploy, deploy.Server)
	}
	version := h.publisher.Versions[sites.ScriptName(site)]
	if version.MainModule != "worker.js" || version.Assets.NotFoundHandling != cfworkers.NotFoundNone ||
		version.CompatibilityDate != "2025-09-01" || strings.Join(version.CompatibilityFlags, ",") != "nodejs_compat" {
		t.Fatalf("version = %+v", version)
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
	off, err := sites.NewService(h.store, h.publisher, h.origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(), Flags: features.NewStatic(), Code: h.store})
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
	service, err := sites.NewService(store, publisher, sitestest.NewOrigins(), sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(),
		Flags: features.NewStatic(features.SitesServer), Code: failingCode{store}})
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
