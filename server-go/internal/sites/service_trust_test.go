package sites_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

// EXC-899: a server site publishes customer code as a Worker, so a limited
// owner cannot deploy or roll one back. A static site runs no code and stays open.

type ownerTrust struct{ state trust.State }

func (o ownerTrust) ForProject(context.Context, string) (trust.State, error) { return o.state, nil }

func gatedOver(t *testing.T, h *serviceHarness, state trust.State) *sites.Service {
	t.Helper()
	options := serverOptions(t, features.NewStatic(features.SitesServer), h.store)
	options.ContainerGate = trust.NewGate(ownerTrust{state}, trust.Policy{MinAgeDays: 90})
	service, err := sites.NewService(h.store, h.publisher, h.origins, options)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func trustServerArchive(t *testing.T) []byte { return sitestest.ServerDist(t, "export default 1") }

func TestServerSiteDeployAndRollbackAreRefusedForALimitedOwner(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	first, err := h.service.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(trustServerArchive(t))})
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	published := len(h.publisher.Published)
	limited := gatedOver(t, h, trust.Limited)

	_, err = limited.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(trustServerArchive(t))})
	if !errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("Deploy err = %v, want ErrVerificationRequired", err)
	}
	if _, err := limited.Rollback(context.Background(), testProject, site.ID, first.ID); !errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("Rollback err = %v, want ErrVerificationRequired", err)
	}
	if len(h.publisher.Published) != published {
		t.Errorf("published %d scripts after the refusals, want %d", len(h.publisher.Published), published)
	}
}

func TestServerSiteDeployAndRollbackPassForAVerifiedOwner(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	first, err := h.service.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(trustServerArchive(t))})
	if err != nil {
		t.Fatal(err)
	}
	full := gatedOver(t, h, trust.Full)
	if _, err := full.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(trustServerArchive(t))}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := full.Rollback(context.Background(), testProject, site.ID, first.ID); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestStaticSiteStaysOpenToALimitedOwner(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	first, err := h.deploy(t, site, viteDist(t))
	if err != nil {
		t.Fatal(err)
	}
	options := sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(), ContainerGate: trust.NewGate(ownerTrust{trust.Limited}, trust.Policy{})}
	limited, err := sites.NewService(h.store, h.publisher, h.origins, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(viteDist(t))}); err != nil {
		t.Fatalf("static deploy refused: %v", err)
	}
	if _, err := limited.Rollback(context.Background(), testProject, site.ID, first.ID); err != nil {
		t.Fatalf("static rollback refused: %v", err)
	}
}
