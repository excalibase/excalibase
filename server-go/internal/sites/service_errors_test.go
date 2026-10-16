package sites_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

var errStoreDown = errors.New("platform database unreachable")

// brokenStore fails every call, so each path that reads or writes reports it.
type brokenStore struct{ sites.Store }

func (brokenStore) Create(context.Context, *sites.Site, int) error { return errStoreDown }
func (brokenStore) Get(context.Context, string, string) (*sites.Site, error) {
	return nil, errStoreDown
}
func (brokenStore) List(context.Context, string) ([]*sites.Site, error) { return nil, errStoreDown }
func (brokenStore) GetDeploy(context.Context, string, string, string) (*sites.Deploy, error) {
	return nil, errStoreDown
}

func TestNewServiceRefusesMissingParts(t *testing.T) {
	if _, err := sites.NewService(nil, nil, sitestest.NewOrigins(), sites.Options{}); err == nil {
		t.Error("a service without a store was built")
	}
	if _, err := sites.NewService(sitestest.NewMemoryStore(), nil, nil, sites.Options{}); err == nil {
		t.Error("a service without the CORS list was built")
	}
	if _, err := sites.NewService(sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins(), sites.Options{}); err == nil {
		t.Error("a publishing service without a base domain was built")
	}
}

func TestEveryCallRefusesWhenUnavailable(t *testing.T) {
	service, err := sites.NewService(sitestest.NewMemoryStore(), nil, sitestest.NewOrigins(), sites.Options{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	calls := map[string]func() error{
		"list":     func() error { _, err := service.List(ctx, testProject); return err },
		"get":      func() error { _, err := service.Get(ctx, testProject, "s"); return err },
		"settings": func() error { _, err := service.UpdateSettings(ctx, testProject, "s", true); return err },
		"deploys":  func() error { _, err := service.ListDeploys(ctx, testProject, "s"); return err },
		"deploy":   func() error { _, err := service.GetDeploy(ctx, testProject, "s", "d"); return err },
		"delete":   func() error { return service.Delete(ctx, testProject, "s") },
		"upload": func() error {
			_, err := service.Deploy(ctx, sites.DeployRequest{ProjectID: testProject, SiteID: "s", Body: bytes.NewReader(nil)})
			return err
		},
		"rollback": func() error { _, err := service.Rollback(ctx, testProject, "s", "d"); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, sites.ErrSitesUnavailable) {
			t.Errorf("%s: err = %v, want unavailable", name, err)
		}
	}
}

func TestStoreFailuresAreReported(t *testing.T) {
	service, err := sites.NewService(brokenStore{}, sitestest.NewPublisher(), sitestest.NewOrigins(), sites.Options{BaseDomain: testDomain})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	calls := map[string]func() error{
		"create":   func() error { _, err := service.Create(ctx, testProject, "shop", nil, testActor); return err },
		"get":      func() error { _, err := service.Get(ctx, testProject, "s"); return err },
		"deploys":  func() error { _, err := service.ListDeploys(ctx, testProject, "s"); return err },
		"deploy":   func() error { _, err := service.GetDeploy(ctx, testProject, "s", "d"); return err },
		"rollback": func() error { _, err := service.Rollback(ctx, testProject, "s", "d"); return err },
		"purge":    func() error { _, err := service.PurgeProjectSites(ctx, testProject); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, errStoreDown) {
			t.Errorf("%s: err = %v, want the store failure", name, err)
		}
	}
}

func TestMissingSiteIsNotFound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.service.Get(ctx, testProject, "missing"); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := h.service.ListDeploys(ctx, testProject, "missing"); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Errorf("deploys: %v", err)
	}
	if err := h.service.Delete(ctx, testProject, "missing"); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Errorf("delete: %v", err)
	}
	if _, err := h.service.Rollback(ctx, testProject, "missing", "d"); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Errorf("rollback: %v", err)
	}
}

func TestDeleteRefusesWhileADeployRuns(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	if err := h.store.ClaimDeploy(context.Background(), testProject, site.ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := h.service.Delete(context.Background(), testProject, site.ID); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("err = %v, want busy", err)
	}
}

func TestPublishErrorsUnwrapToTheCause(t *testing.T) {
	cause := errors.New("cloudflare answered 500")
	err := error(&sites.PublishError{Err: cause})
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "cloudflare answered 500") {
		t.Fatalf("err = %v", err)
	}
	if (&sites.InputError{Reason: "bad name"}).Error() != "bad name" {
		t.Fatal("an input error must read as its reason")
	}
}

func TestALongFailureReasonIsCutToValidText(t *testing.T) {
	h := newHarness(t)
	h.publisher.FailPut = errors.New(strings.Repeat("é", 400))
	site := h.createSite(t, "shop")
	if _, err := h.deploy(t, site, viteDist(t)); err == nil {
		t.Fatal("want the publish failure")
	}
	deploys, _ := h.service.ListDeploys(context.Background(), testProject, site.ID)
	reason := deploys[0].FailureReason
	if len(reason) > 500 || !strings.HasSuffix(reason, "é") {
		t.Fatalf("reason has %d bytes, ends %q", len(reason), reason[len(reason)-3:])
	}
}

func TestDeployWithoutASpoolDirUsesTheSystemTemp(t *testing.T) {
	service, err := sites.NewService(sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins(), sites.Options{BaseDomain: testDomain})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	site, err := service.Create(context.Background(), testProject, "shop", nil, testActor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := service.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Body: bytes.NewReader(viteDist(t))}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
}
