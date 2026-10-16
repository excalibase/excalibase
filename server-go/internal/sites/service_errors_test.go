package sites_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
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
	if err := h.store.ClaimDeploy(context.Background(), testProject, site.ID, "other-holder", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := h.service.Delete(context.Background(), testProject, site.ID); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("err = %v, want busy", err)
	}
}

func TestPublishErrorsUnwrapToTheCause(t *testing.T) {
	cause := errors.New("cloudflare answered 500")
	err := error(&sites.PublishError{Step: "publish the site", Err: cause})
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "cloudflare answered 500") {
		t.Fatalf("err = %v", err)
	}
	if (&sites.InputError{Reason: "bad name"}).Error() != "bad name" {
		t.Fatal("an input error must read as its reason")
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

// Cloudflare's errors name the account and namespace in their paths; viewers read failure reasons.
func TestPublishFailuresNeverShowTheAccountOrNamespace(t *testing.T) {
	leaky := []error{
		errors.New("call cloudflare PUT /client/v4/accounts/acct-9f3e/workers/dispatch/namespaces/ns-prod-7/scripts/site-x: dial tcp: timeout"),
		&cfworkers.APIError{Status: 400, Codes: []int{10021}, Message: "namespace ns-prod-7 in account acct-9f3e: bad jwt"},
	}
	for _, cause := range leaky {
		h := newHarness(t)
		h.publisher.FailPut = cause
		site := h.createSite(t, "shop")
		_, err := h.deploy(t, site, viteDist(t))
		deploys, _ := h.service.ListDeploys(context.Background(), testProject, site.ID)
		for _, shown := range []string{err.Error(), deploys[0].FailureReason} {
			if strings.Contains(shown, "acct-9f3e") || strings.Contains(shown, "ns-prod-7") {
				t.Errorf("shown to users: %q", shown)
			}
			if !strings.Contains(shown, "publish the site") {
				t.Errorf("message %q does not name the step", shown)
			}
		}
	}
}

func TestARefusalNamesCloudflaresCode(t *testing.T) {
	h := newHarness(t)
	h.publisher.FailPut = &cfworkers.APIError{Status: 400, Codes: []int{10021}, Message: "bad jwt"}
	site := h.createSite(t, "shop")
	_, err := h.deploy(t, site, viteDist(t))
	if err == nil || err.Error() != "the CDN refused to publish the site (code 10021)" {
		t.Fatalf("err = %v", err)
	}
}
