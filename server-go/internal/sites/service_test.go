package sites_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

const (
	testProject = "proj-sites1"
	testDomain  = "excalibase.app"
	testActor   = "user-dev"
)

type serviceHarness struct {
	service   *sites.Service
	store     *sitestest.MemoryStore
	publisher *sitestest.Publisher
	origins   *sitestest.Origins
}

func newHarness(t *testing.T, existingOrigins ...string) *serviceHarness {
	t.Helper()
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins(existingOrigins...)
	service, err := sites.NewService(store, publisher, origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &serviceHarness{service: service, store: store, publisher: publisher, origins: origins}
}

func (h *serviceHarness) createSite(t *testing.T, name string) *sites.Site {
	t.Helper()
	site, err := h.service.Create(context.Background(), testProject, name, nil, testActor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return site
}

func (h *serviceHarness) deploy(t *testing.T, site *sites.Site, archive []byte) (*sites.Deploy, error) {
	t.Helper()
	return h.service.Deploy(context.Background(), sites.DeployRequest{
		ProjectID: testProject, SiteID: site.ID, Actor: testActor, Label: "main@abc", Body: bytes.NewReader(archive),
	})
}

func TestCreateNamesTheHostnameUnderTheBaseDomain(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	if site.Hostname != "shop.excalibase.app" || !site.SPAFallback || site.ProjectID != testProject || site.ID == "" {
		t.Fatalf("site = %+v", site)
	}
}

func TestCreateRefusesABadNameAndTheLimit(t *testing.T) {
	h := newHarness(t)
	if _, err := h.service.Create(context.Background(), testProject, "Bad Name", nil, testActor); err == nil {
		t.Fatal("a bad name was accepted")
	}
	for i := range sites.DefaultLimits.MaxSites {
		h.createSite(t, "site"+string(rune('a'+i)))
	}
	_, err := h.service.Create(context.Background(), testProject, "one-more", nil, testActor)
	if !errors.Is(err, sites.ErrSiteLimitReached) {
		t.Fatalf("err = %v, want the limit", err)
	}
}

func TestDeployUploadsPublishesAndGoesLive(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	deploy, err := h.deploy(t, site, viteDist(t))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if deploy.Status != sites.DeploySucceeded || deploy.FileCount != 3 || deploy.Label != "main@abc" || deploy.CreatedBy != testActor {
		t.Fatalf("deploy = %+v", deploy)
	}
	if len(h.publisher.Stored) != 3 {
		t.Errorf("uploaded %d files, want 3", len(h.publisher.Stored))
	}
	if h.publisher.Published[sites.ScriptName(site)].NotFoundHandling != "single-page-application" {
		t.Errorf("published = %+v", h.publisher.Published)
	}
	stored, _ := h.store.Get(context.Background(), testProject, site.ID)
	if stored.LiveDeployID != deploy.ID {
		t.Errorf("live = %q, want %q", stored.LiveDeployID, deploy.ID)
	}
}

func TestDeployWithoutSPAPublishesThe404Page(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "docs-site")
	if _, err := h.service.UpdateSettings(context.Background(), testProject, site.ID, false); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if h.publisher.Published[sites.ScriptName(site)].NotFoundHandling != "404-page" {
		t.Errorf("published = %+v", h.publisher.Published)
	}
}

func TestFirstDeployAddsTheOriginOnceAndDeleteReleasesIt(t *testing.T) {
	h := newHarness(t, "http://localhost:5173")
	site := h.createSite(t, "shop")
	for range 2 {
		if _, err := h.deploy(t, site, viteDist(t)); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
	}
	if strings.Join(h.origins.Origins, ",") != "http://localhost:5173,https://shop.excalibase.app" {
		t.Fatalf("origins = %v", h.origins.Origins)
	}
	if err := h.service.Delete(context.Background(), testProject, site.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if strings.Join(h.origins.Origins, ",") != "http://localhost:5173" {
		t.Fatalf("origins after delete = %v", h.origins.Origins)
	}
	if len(h.publisher.Deleted) != 1 || h.publisher.Deleted[0] != sites.ScriptName(site) {
		t.Fatalf("deleted = %v", h.publisher.Deleted)
	}
}

func TestDeleteKeepsAnOriginTheUserAddedFirst(t *testing.T) {
	h := newHarness(t, "https://shop.excalibase.app")
	site := h.createSite(t, "shop")
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := h.service.Delete(context.Background(), testProject, site.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if strings.Join(h.origins.Origins, ",") != "https://shop.excalibase.app" {
		t.Fatalf("origins = %v", h.origins.Origins)
	}
}

func TestACorsFailureStopsTheDeployBeforePublishing(t *testing.T) {
	h := newHarness(t)
	h.origins.FailAdd = errors.New("cors store down")
	site := h.createSite(t, "shop")
	if _, err := h.deploy(t, site, viteDist(t)); err == nil {
		t.Fatal("want the CORS failure reported")
	}
	if h.publisher.Sessions != 0 || len(h.publisher.Published) != 0 {
		t.Fatal("nothing may be published when the origin could not be added")
	}
}

func TestAFailedPublishIsRecordedAndNothingGoesLive(t *testing.T) {
	h := newHarness(t)
	h.publisher.FailPut = errors.New("cloudflare answered 500")
	site := h.createSite(t, "shop")
	_, err := h.deploy(t, site, viteDist(t))
	var publishErr *sites.PublishError
	if !errors.As(err, &publishErr) {
		t.Fatalf("err = %v, want a sites.PublishError", err)
	}
	deploys, _ := h.service.ListDeploys(context.Background(), testProject, site.ID)
	if len(deploys) != 1 || deploys[0].Status != sites.DeployFailed || deploys[0].FailureReason == "" {
		t.Fatalf("deploys = %+v", deploys)
	}
	stored, _ := h.store.Get(context.Background(), testProject, site.ID)
	if stored.LiveDeployID != "" {
		t.Fatalf("live = %q after a failed deploy", stored.LiveDeployID)
	}
}

func TestAnInvalidArchiveCreatesNoDeploy(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	_, err := h.deploy(t, site, []byte("nope, not a tar archive, just enough text to fill a block"))
	var archiveErr *sites.ArchiveError
	if !errors.As(err, &archiveErr) {
		t.Fatalf("err = %v, want an sites.ArchiveError", err)
	}
	deploys, _ := h.service.ListDeploys(context.Background(), testProject, site.ID)
	if len(deploys) != 0 {
		t.Fatalf("deploys = %+v", deploys)
	}
}

func TestDeployRefusesWhileTheSiteIsBusy(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	if err := h.store.ClaimDeploy(context.Background(), testProject, site.ID, "other-holder", h.store.Now().Add(sites.MaxDeployDuration)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := h.deploy(t, site, viteDist(t)); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("err = %v, want busy", err)
	}
}

func TestDeployToAnotherProjectsSiteIsNotFound(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	_, err := h.service.Deploy(context.Background(), sites.DeployRequest{ProjectID: "proj-other", SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(viteDist(t))})
	if !errors.Is(err, sites.ErrSiteNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestRollbackRepublishesAnEarlierDeploy(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	first, err := h.deploy(t, site, viteDist(t))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := h.deploy(t, site, distWith(t, "<h1>v2</h1>")); err != nil {
		t.Fatalf("second: %v", err)
	}
	rolled, err := h.service.Rollback(context.Background(), testProject, site.ID, first.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolled.LiveDeployID != first.ID {
		t.Fatalf("live = %q, want %q", rolled.LiveDeployID, first.ID)
	}
}

func TestRollbackFailsClearlyWhenCloudflareAsksForFiles(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	first, err := h.deploy(t, site, viteDist(t))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	h.publisher.Forget = true
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, first.ID); !errors.Is(err, sites.ErrRollbackNeedsFiles) {
		t.Fatalf("err = %v, want sites.ErrRollbackNeedsFiles", err)
	}
}

func TestRollbackRefusesAFailedOrUnknownDeploy(t *testing.T) {
	h := newHarness(t)
	h.publisher.FailPut = errors.New("down")
	site := h.createSite(t, "shop")
	failed, _ := h.deploy(t, site, viteDist(t))
	deploys, _ := h.service.ListDeploys(context.Background(), testProject, site.ID)
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, deploys[0].ID); !errors.Is(err, sites.ErrDeployNotServable) {
		t.Fatalf("err = %v, want not servable (deploy %+v)", err, failed)
	}
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, "missing"); !errors.Is(err, sites.ErrDeployNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestUnavailableServiceRefusesEverything(t *testing.T) {
	service, err := sites.NewService(sitestest.NewMemoryStore(), nil, sitestest.NewOrigins(), sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if service.Available() {
		t.Fatal("a service without a publisher reports itself available")
	}
	if _, err := service.Create(context.Background(), testProject, "shop", nil, testActor); !errors.Is(err, sites.ErrSitesUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestPurgeProjectSitesDeletesEveryScript(t *testing.T) {
	h := newHarness(t)
	shop, blog := h.createSite(t, "shop"), h.createSite(t, "journal")
	removed, err := h.service.PurgeProjectSites(context.Background(), testProject)
	if err != nil || removed != 2 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if len(h.publisher.Deleted) != 2 {
		t.Fatalf("deleted = %v (sites %s, %s)", h.publisher.Deleted, shop.ID, blog.ID)
	}
}

func TestPurgeWithoutCloudflareRefusesWhenSitesExist(t *testing.T) {
	store := sitestest.NewMemoryStore()
	if err := store.Create(context.Background(), &sites.Site{ID: "s1", ProjectID: testProject, Name: "shop"}, 5, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	service, _ := sites.NewService(store, nil, sitestest.NewOrigins(), sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir()})
	if _, err := service.PurgeProjectSites(context.Background(), testProject); !errors.Is(err, sites.ErrSitesUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if removed, err := service.PurgeProjectSites(context.Background(), "proj-empty"); err != nil || removed != 0 {
		t.Fatalf("an empty project: removed=%d err=%v", removed, err)
	}
}

func viteDist(t *testing.T) []byte {
	return sitestest.Dist(t, "<!doctype html><div id=app></div>")
}

func distWith(t *testing.T, index string) []byte {
	return sitestest.Dist(t, index)
}

type fixedLimits struct{ limits sites.Limits }

func (f fixedLimits) DeployLimits(context.Context, string) (sites.Limits, error) {
	return f.limits, nil
}

func TestDeployHoldsTheArchiveToTheProjectsLimits(t *testing.T) {
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins()
	service, err := sites.NewService(store, publisher, origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(),
		DeployLimits: fixedLimits{sites.Limits{MaxFiles: 2, MaxFileBytes: 1 << 20, MaxTotalBytes: 1 << 20, MaxSites: 1, MaxDeploysPerHour: 1}}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	site, err := service.Create(context.Background(), testProject, "shop", nil, testActor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = service.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Body: bytes.NewReader(viteDist(t))})
	var archiveErr *sites.ArchiveError
	if !errors.As(err, &archiveErr) || !archiveErr.TooLarge {
		t.Fatalf("err = %v, want the project's file limit", err)
	}
	ceiling, err := service.MaxUploadBytes(context.Background(), testProject)
	if err != nil || ceiling <= 1<<20 {
		t.Fatalf("ceiling = %d err=%v, want room for tar framing above the total", ceiling, err)
	}
}
