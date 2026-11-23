package sites_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

// guardedOver is a second service over the harness's store, whose project's
// org owner is (or is not) suspended.
func guardedOver(t *testing.T, h *serviceHarness, suspended bool) *sites.Service {
	t.Helper()
	instances := fakestore.NewInstances()
	instances.Items[testProject] = &domain.DatabaseInstance{ProjectID: testProject, OrgID: "org-1"}
	guard := orgguard.New(instances, func(context.Context) (map[string]bool, error) {
		return map[string]bool{"org-1": suspended}, nil
	})
	service, err := sites.NewService(h.store, h.publisher, h.origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(), OrgGuard: guard})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func TestDeployAndRollback_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	first, err := h.deploy(t, site, viteDist(t))
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if _, err := h.deploy(t, site, distWith(t, "<h1>v2</h1>")); err != nil {
		t.Fatalf("second deploy: %v", err)
	}
	published := len(h.publisher.Published)
	guarded := guardedOver(t, h, true)

	_, err = guarded.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(viteDist(t))})
	if !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("Deploy err = %v, want ErrOrgSuspended", err)
	}
	if _, err := guarded.Rollback(context.Background(), testProject, site.ID, first.ID); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("Rollback err = %v, want ErrOrgSuspended", err)
	}
	if len(h.publisher.Published) != published {
		t.Errorf("published %d scripts after the refusals, want %d", len(h.publisher.Published), published)
	}
}

func TestDeployAndRollback_APlatformAdminAndAnUnsuspendedOrgAreUnaffected(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	first, err := h.deploy(t, site, viteDist(t))
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	admin := orgguard.WithPlatformOverride(context.Background())
	suspended := guardedOver(t, h, true)
	if _, err := suspended.Deploy(admin, sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(viteDist(t))}); err != nil {
		t.Fatalf("platform admin deploy: %v", err)
	}
	if _, err := suspended.Rollback(admin, testProject, site.ID, first.ID); err != nil {
		t.Fatalf("platform admin rollback: %v", err)
	}
	clear := guardedOver(t, h, false)
	if _, err := clear.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(viteDist(t))}); err != nil {
		t.Fatalf("unsuspended deploy: %v", err)
	}
}

func TestDeployPreview_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins()
	previews := sitestest.NewMemoryPreviews(store)
	instances := fakestore.NewInstances()
	instances.Items[testProject] = &domain.DatabaseInstance{ProjectID: testProject, OrgID: "org-1"}
	guard := orgguard.New(instances, func(context.Context) (map[string]bool, error) { return map[string]bool{"org-1": true}, nil })
	service, err := sites.NewService(store, publisher, origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(), Previews: previews, OrgGuard: guard})
	if err != nil {
		t.Fatal(err)
	}
	site, err := service.Create(context.Background(), testProject, "shop", nil, testActor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.DeployPreview(context.Background(), sites.PreviewDeployRequest{ProjectID: testProject, SiteID: site.ID,
		PreviewID: "any", CommitSHA: "abc1234def", Body: bytes.NewReader(sitestest.Dist(t, "<h1>x</h1>"))})
	if !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("DeployPreview err = %v, want ErrOrgSuspended", err)
	}
	if len(publisher.Published) != 0 {
		t.Errorf("published %v, want nothing", publisher.Published)
	}
}
