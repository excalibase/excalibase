package sites_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

const testTarget = "cname.excalibase.app"

type domainHarness struct {
	*serviceHarness
	zone      *sitestest.Zone
	hostnames *sitestest.MemoryHostnames
	resolver  *sitestest.Resolver
	projects  *sitestest.Projects
	clock     *time.Time
	// unproven leaves the owner's TXT record out when a hostname is added.
	unproven bool
}

func newDomainHarness(t *testing.T, existingOrigins ...string) *domainHarness {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins(existingOrigins...)
	store.Now = clock
	zone, hostnames, resolver, projects := sitestest.NewZone(), sitestest.NewMemoryHostnames(), sitestest.NewResolver(), sitestest.NewProjects()
	service, err := sites.NewService(store, publisher, origins, sites.Options{
		BaseDomain: testDomain, SpoolDir: t.TempDir(),
		CustomDomains: &sites.CustomDomainOptions{Zone: zone, Target: testTarget, ReservedDomains: []string{"excalibase.io"},
			Resolver: resolver, Projects: projects},
		Hostnames: hostnames,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := &domainHarness{serviceHarness: &serviceHarness{service: service, store: store, publisher: publisher, origins: origins},
		zone: zone, hostnames: hostnames, resolver: resolver, projects: projects, clock: &now}
	service.SetNow(clock)
	return h
}

// withLimits rebuilds the harness's service on the same fakes with the plan's limits.
func (h *domainHarness) withLimits(t *testing.T, limits sites.DeployLimiter) *sites.Service {
	t.Helper()
	service, err := sites.NewService(h.store, h.publisher, h.origins, sites.Options{
		BaseDomain: testDomain, SpoolDir: t.TempDir(), DeployLimits: limits,
		CustomDomains: &sites.CustomDomainOptions{Zone: h.zone, Target: testTarget, Resolver: h.resolver, Projects: h.projects},
		Hostnames:     h.hostnames,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.SetNow(func() time.Time { return *h.clock })
	return service
}

func (h *domainHarness) add(t *testing.T, site *sites.Site, hostname string) *sites.CustomHostname {
	t.Helper()
	added, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: hostname, Actor: testActor,
	})
	if err != nil {
		t.Fatalf("AddHostname(%s): %v", hostname, err)
	}
	if !h.unproven {
		h.prove(t, added)
	}
	return added
}

// prove adds the TXT record the platform asked the owner for.
func (h *domainHarness) prove(t *testing.T, added *sites.CustomHostname) {
	t.Helper()
	for _, record := range added.Records {
		if record.Purpose == sites.PurposeVerification {
			h.resolver.Set(record.Name, record.Value)
			return
		}
	}
	t.Fatalf("no verification record in %+v", added.Records)
}

func (h *domainHarness) refresh(t *testing.T, site *sites.Site, id string) *sites.CustomHostname {
	t.Helper()
	*h.clock = h.clock.Add(time.Minute)
	refreshed, err := h.service.RefreshHostname(context.Background(), testProject, site.ID, id)
	if err != nil {
		t.Fatalf("RefreshHostname: %v", err)
	}
	return refreshed
}

func (h *domainHarness) liveSite(t *testing.T, name string) *sites.Site {
	t.Helper()
	site := h.createSite(t, name)
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	return site
}

func TestAddHostnameCreatesACustomHostnameValidatedOverHTTPAndRoutesNothingYet(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "WWW.Example.com")
	if added.Hostname != "www.example.com" || added.Status != sites.HostnamePending || added.Validation != "http" {
		t.Fatalf("added = %+v", added)
	}
	created := h.zone.Created()
	if len(created) != 1 || created[0] != "www.example.com/http" {
		t.Fatalf("cloudflare got %v", created)
	}
	if len(added.Records) != 2 || added.Records[0].Type != "CNAME" || added.Records[0].Value != testTarget {
		t.Fatalf("records = %+v", added.Records)
	}
	if verify := added.Records[1]; verify.Type != "TXT" || verify.Name != "_excalibase-challenge.www.example.com" ||
		!strings.HasPrefix(verify.Value, "excalibase-verification=") || verify.Purpose != sites.PurposeVerification {
		t.Fatalf("verification record = %+v", verify)
	}
	if _, routed := h.publisher.Hosts["www.example.com"]; routed {
		t.Fatal("a pending hostname was written to the hosts table")
	}
}

func TestAddHostnameWithTXTShowsTheValidationRecords(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "www.example.com", Validation: "txt", Actor: testActor,
	})
	if err != nil {
		t.Fatalf("AddHostname: %v", err)
	}
	types := []string{}
	for _, record := range added.Records {
		types = append(types, record.Type+":"+record.Purpose)
	}
	if strings.Join(types, ",") != "CNAME:routing,TXT:verification,TXT:ownership,TXT:certificate" {
		t.Fatalf("records = %v", types)
	}
	if h.zone.Created()[0] != "www.example.com/txt" {
		t.Fatalf("cloudflare got %v", h.zone.Created())
	}
}

func TestAddHostnameRefusesBadInput(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	for _, request := range []sites.AddHostnameRequest{
		{Hostname: "1.2.3.4"}, {Hostname: "other.excalibase.app"}, {Hostname: "www.excalibase.io"},
		{Hostname: "example.com"}, {Hostname: "www.example.com", Validation: "email"},
	} {
		request.ProjectID, request.SiteID, request.Actor = testProject, site.ID, testActor
		_, err := h.service.AddHostname(context.Background(), request)
		var inputErr *sites.InputError
		if !errors.As(err, &inputErr) {
			t.Errorf("%+v: err = %v, want an input refusal", request, err)
		}
	}
	if len(h.zone.Created()) != 0 {
		t.Fatalf("cloudflare was called: %v", h.zone.Created())
	}
}

func TestAHostnameIsHeldByOneProjectOnly(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	h.add(t, site, "www.example.com")
	other := &sites.Site{ID: "other-site", ProjectID: "proj-other", Name: "other", Hostname: "other.excalibase.app"}
	if err := h.store.Create(context.Background(), other, 5); err != nil {
		t.Fatal(err)
	}
	_, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: "proj-other", SiteID: other.ID, Hostname: "www.example.com", Actor: testActor,
	})
	if !errors.Is(err, sites.ErrHostnameTaken) {
		t.Fatalf("err = %v, want taken", err)
	}
	if len(h.zone.Created()) != 1 {
		t.Fatalf("cloudflare got a second create: %v", h.zone.Created())
	}
}

func TestAProjectHoldsAtMostThePlansLimitAcrossItsSites(t *testing.T) {
	h := newDomainHarness(t)
	shop, blog := h.liveSite(t, "shop"), h.liveSite(t, "journal")
	h.add(t, shop, "www.example.com")
	h.add(t, blog, "blog.example.com")
	_, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: blog.ID, Hostname: "news.example.com", Actor: testActor,
	})
	if !errors.Is(err, sites.ErrHostnameLimitReached) || !strings.Contains(err.Error(), "at most 2") {
		t.Fatalf("err = %v, want the FREE plan's limit of 2", err)
	}
}

// The limit is the plan's (EXC-620's tier config), so a bigger plan holds more and 0 offers none.
func TestTheCustomDomainLimitFollowsThePlan(t *testing.T) {
	limits := sites.DefaultLimits
	limits.MaxSites, limits.MaxCustomDomains = 5, 3
	h := newDomainHarness(t)
	h.service = h.withLimits(t, fixedLimits{limits})
	shop := h.liveSite(t, "shop")
	for _, host := range []string{"a.example.com", "b.example.com", "c.example.com"} {
		h.add(t, shop, host)
	}
	if _, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: shop.ID, Hostname: "d.example.com", Actor: testActor,
	}); !errors.Is(err, sites.ErrHostnameLimitReached) || !strings.Contains(err.Error(), "at most 3") {
		t.Fatalf("err = %v, want the plan's limit of 3", err)
	}
	limits.MaxCustomDomains = 0
	none := newDomainHarness(t)
	none.service = none.withLimits(t, fixedLimits{limits})
	site := none.liveSite(t, "shop")
	if _, err := none.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "a.example.com", Actor: testActor,
	}); !errors.Is(err, sites.ErrHostnameLimitReached) || len(none.zone.Created()) != 0 {
		t.Fatalf("err = %v created = %v, want a plan without custom domains refused before Cloudflare", err, none.zone.Created())
	}
}

func TestACloudflareRefusalOnAddLeavesNoClaim(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	h.zone.FailCreate = errors.New("cloudflare down")
	_, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "www.example.com", Actor: testActor,
	})
	var publishErr *sites.PublishError
	if !errors.As(err, &publishErr) {
		t.Fatalf("err = %v, want the CDN failure", err)
	}
	if listed, _ := h.service.ListHostnames(context.Background(), testProject, site.ID); len(listed) != 0 {
		t.Fatalf("hostnames = %+v, want the claim dropped", listed)
	}
}

// A create that reached Cloudflare but whose answer was lost is adopted on the next try, not refused.
func TestAddHostnameAdoptsTheZonesOwnCopyOfAHostnameItHolds(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	existing := h.zone.Seed("www.example.com", "pending", "pending_validation")
	added := h.add(t, site, "www.example.com")
	if stored := h.hostnames.CloudflareID(added.ID); stored != existing {
		t.Fatalf("cloudflare id = %q, want the adopted %q", stored, existing)
	}
}

func TestRefreshRoutesTheHostnameOnceItAndItsCertificateAreActive(t *testing.T) {
	h := newDomainHarness(t, "http://localhost:5173")
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "pending_validation")
	if got := h.refresh(t, site, added.ID); got.Status != sites.HostnamePending {
		t.Fatalf("status = %s with the certificate still pending", got.Status)
	}
	if _, routed := h.publisher.Hosts["www.example.com"]; routed {
		t.Fatal("routed before the certificate was active")
	}
	if h.zone.Revalidations() != 1 {
		t.Fatalf("revalidations = %d, want a nudge while pending", h.zone.Revalidations())
	}
	h.zone.SetState("www.example.com", "active", "active")
	got := h.refresh(t, site, added.ID)
	if got.Status != sites.HostnameActive || h.publisher.Hosts["www.example.com"] != sites.ScriptName(site) {
		t.Fatalf("status = %s hosts = %v", got.Status, h.publisher.Hosts)
	}
	if !strings.Contains(strings.Join(h.origins.Origins, ","), "https://www.example.com") {
		t.Fatalf("origins = %v, want the custom origin added", h.origins.Origins)
	}
}

func TestAnActiveHostnameOfASiteNeverDeployedWaitsForTheFirstDeploy(t *testing.T) {
	h := newDomainHarness(t)
	site := h.createSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	if got := h.refresh(t, site, added.ID); got.Status != sites.HostnameActive {
		t.Fatalf("status = %s", got.Status)
	}
	if _, routed := h.publisher.Hosts["www.example.com"]; routed {
		t.Fatal("routed to a Worker that does not exist yet")
	}
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if h.publisher.Hosts["www.example.com"] != sites.ScriptName(site) {
		t.Fatalf("hosts = %v, want the deploy to route the active hostname", h.publisher.Hosts)
	}
}

func TestAHostnameThatStopsBeingActiveLeavesTheRouter(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	h.refresh(t, site, added.ID)
	h.zone.SetState("www.example.com", "moved", "active")
	if got := h.refresh(t, site, added.ID); got.Status != sites.HostnameFailed {
		t.Fatalf("status = %s", got.Status)
	}
	if _, routed := h.publisher.Hosts["www.example.com"]; routed {
		t.Fatal("a moved hostname is still routed")
	}
}

func TestRefreshWithinSecondsAnswersTheStoredStateWithoutCallingCloudflare(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	reads := h.zone.Reads()
	if _, err := h.service.RefreshHostname(context.Background(), testProject, site.ID, added.ID); err != nil {
		t.Fatalf("RefreshHostname: %v", err)
	}
	if h.zone.Reads() != reads {
		t.Fatal("a refresh right after the last check called Cloudflare again")
	}
}

// The router stops sending traffic first, then the hostname and its certificate go.
func TestRemoveHostnameUnroutesThenDeletesTheCustomHostname(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	h.refresh(t, site, added.ID)
	h.publisher.Calls, h.zone.Log = nil, nil
	if err := h.service.RemoveHostname(context.Background(), testProject, site.ID, added.ID); err != nil {
		t.Fatalf("RemoveHostname: %v", err)
	}
	if strings.Join(h.publisher.Calls, ",") != "delete-host" || strings.Join(h.zone.Log, ",") != "delete www.example.com" {
		t.Fatalf("calls = %v zone = %v", h.publisher.Calls, h.zone.Log)
	}
	if h.zone.Holds("www.example.com") || strings.Contains(strings.Join(h.origins.Origins, ","), "www.example.com") {
		t.Fatalf("zone or CORS still hold the hostname: %v", h.origins.Origins)
	}
	if _, err := h.service.RefreshHostname(context.Background(), testProject, site.ID, added.ID); !errors.Is(err, sites.ErrHostnameNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestACloudflareFailureOnRemoveKeepsTheRecordToRetry(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.FailDelete = errors.New("down")
	if err := h.service.RemoveHostname(context.Background(), testProject, site.ID, added.ID); err == nil {
		t.Fatal("want the failure")
	}
	if listed, _ := h.service.ListHostnames(context.Background(), testProject, site.ID); len(listed) != 1 {
		t.Fatalf("hostnames = %+v", listed)
	}
}

func TestDeletingASiteRemovesItsHostnamesFirst(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	h.refresh(t, site, added.ID)
	if err := h.service.Delete(context.Background(), testProject, site.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if h.zone.Holds("www.example.com") || len(h.publisher.Hosts) != 0 {
		t.Fatalf("zone holds it %v, hosts %v", h.zone.Holds("www.example.com"), h.publisher.Hosts)
	}
	if listed, _ := h.hostnames.ListProjectHostnames(context.Background(), testProject); len(listed) != 0 {
		t.Fatalf("hostnames left: %+v", listed)
	}
}

func TestPurgeRemovesEveryCustomHostname(t *testing.T) {
	h := newDomainHarness(t)
	shop, blog := h.liveSite(t, "shop"), h.liveSite(t, "journal")
	h.add(t, shop, "www.example.com")
	h.add(t, blog, "blog.example.com")
	if _, err := h.service.PurgeProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if h.zone.Holds("www.example.com") || h.zone.Holds("blog.example.com") {
		t.Fatal("custom hostnames left on the zone")
	}
}

// A project scheduled for deletion is not served on its custom hostnames either; a cancel serves the active ones again.
func TestGracePeriodUnroutesCustomHostnamesAndACancelRoutesTheActiveOnes(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	active := h.add(t, site, "www.example.com")
	h.add(t, site, "pending.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	h.refresh(t, site, active.ID)
	if err := h.service.UnrouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("Unroute: %v", err)
	}
	if len(h.publisher.Hosts) != 0 {
		t.Fatalf("hosts = %v", h.publisher.Hosts)
	}
	if !h.zone.Holds("www.example.com") {
		t.Fatal("the grace period removed the custom hostname; a cancel could not bring it back")
	}
	if err := h.service.RouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(h.publisher.Hosts) != 2 || h.publisher.Hosts["www.example.com"] != sites.ScriptName(site) {
		t.Fatalf("hosts = %v, want the site and its active hostname only", h.publisher.Hosts)
	}
}

func TestRefreshPendingChecksWaitingHostnamesAndDropsAbandonedOnes(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	h.add(t, site, "www.example.com")
	h.add(t, site, "old.example.com")
	h.hostnames.Age("old.example.com", sites.PendingHostnameLifetime+time.Hour)
	h.zone.SetState("www.example.com", "active", "active")
	*h.clock = h.clock.Add(time.Minute)
	report := h.service.RefreshPendingHostnames(context.Background())
	if report.Activated != 1 || report.Expired != 1 || report.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if h.publisher.Hosts["www.example.com"] != sites.ScriptName(site) || h.zone.Holds("old.example.com") {
		t.Fatalf("hosts = %v, old still held = %v", h.publisher.Hosts, h.zone.Holds("old.example.com"))
	}
}

func TestCustomDomainsWithoutAZoneAreNotAvailable(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	_, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "www.example.com", Actor: testActor,
	})
	if !errors.Is(err, sites.ErrCustomDomainsUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestASiteCannotTakeTheCNAMETargetsName(t *testing.T) {
	h := newDomainHarness(t)
	if _, err := h.service.Create(context.Background(), testProject, "cname", nil, testActor); err == nil {
		t.Fatal("a site took the hostname customers point their CNAME at")
	}
}

// A CNAME left behind by an earlier owner validates on Cloudflare by itself, so
// nothing is routed until whoever adds the domain proves it with their own TXT record.
func TestAnActiveHostnameIsNotRoutedUntilItsOwnerProvesTheDomain(t *testing.T) {
	h := newDomainHarness(t)
	h.unproven = true
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	got := h.refresh(t, site, added.ID)
	if got.Status != sites.HostnamePending || got.VerifiedAt != nil {
		t.Fatalf("status = %s verified = %v without the owner's TXT record", got.Status, got.VerifiedAt)
	}
	if _, routed := h.publisher.Hosts["www.example.com"]; routed {
		t.Fatal("routed on a CNAME alone")
	}
	if !strings.Contains(strings.Join(got.Errors, " "), "_excalibase-challenge.www.example.com") {
		t.Fatalf("errors = %v, want the missing TXT record named", got.Errors)
	}
	h.resolver.Set("_excalibase-challenge.www.example.com", "excalibase-verification=someone-elses")
	if got := h.refresh(t, site, added.ID); got.Status != sites.HostnamePending {
		t.Fatalf("another token proved it: %s", got.Status)
	}
	h.prove(t, added)
	got = h.refresh(t, site, added.ID)
	if got.Status != sites.HostnameActive || got.VerifiedAt == nil || h.publisher.Hosts["www.example.com"] != sites.ScriptName(site) {
		t.Fatalf("status = %s hosts = %v", got.Status, h.publisher.Hosts)
	}
	for _, record := range got.Records {
		if record.Purpose == sites.PurposeVerification {
			t.Fatalf("a proven domain still asks for its TXT record: %+v", got.Records)
		}
	}
}

// A create whose answer was lost may still have reached the zone: it is found and removed before the claim goes.
func TestAnUnansweredCreateLeavesNothingOnTheZone(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	h.zone.FailCreateAfterStore = context.DeadlineExceeded
	if _, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "www.example.com", Actor: testActor,
	}); err == nil {
		t.Fatal("want the failure")
	}
	if h.zone.Holds("www.example.com") {
		t.Fatal("the hostname was left on the zone")
	}
	if listed, _ := h.hostnames.ListProjectHostnames(context.Background(), testProject); len(listed) != 0 {
		t.Fatalf("claim kept: %+v", listed)
	}
}

// When the zone cannot be cleaned either, the claim stays so a later check finds the hostname again.
func TestAnUnansweredCreateThatCannotBeCleanedKeepsTheClaim(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	h.zone.FailCreateAfterStore, h.zone.FailDelete = context.DeadlineExceeded, errors.New("down")
	if _, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "www.example.com", Actor: testActor,
	}); err == nil {
		t.Fatal("want the failure")
	}
	listed, _ := h.hostnames.ListProjectHostnames(context.Background(), testProject)
	if len(listed) != 1 || listed[0].Status != sites.HostnamePending {
		t.Fatalf("claim = %+v, want it kept pending", listed)
	}
	h.zone.FailCreateAfterStore, h.zone.FailDelete = nil, nil
	got := h.refresh(t, site, listed[0].ID)
	if h.hostnames.CloudflareID(got.ID) == "" || len(h.zone.Created()) != 0 {
		t.Fatalf("the zone's copy was not adopted: id=%q created=%v", h.hostnames.CloudflareID(got.ID), h.zone.Created())
	}
}

func TestADefiniteRefusalOnAddDropsTheClaim(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	h.zone.FailCreate = &cfworkers.APIError{Status: 400, Codes: []int{1411}, Message: "hostname not allowed"}
	if _, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "www.example.com", Actor: testActor,
	}); err == nil {
		t.Fatal("want the refusal")
	}
	if listed, _ := h.hostnames.ListProjectHostnames(context.Background(), testProject); len(listed) != 0 {
		t.Fatalf("claim kept: %+v", listed)
	}
}

// A sweep that reaches a hostname removed meanwhile does nothing to the zone or the router.
func TestTheSweepRereadsAHostnameOnceItHoldsTheSite(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	h.store.OnClaim = func() {
		h.store.OnClaim = nil
		_ = h.hostnames.DeleteHostname(context.Background(), testProject, site.ID, added.ID)
	}
	*h.clock = h.clock.Add(time.Minute)
	report := h.service.RefreshPendingHostnames(context.Background())
	if report.Activated != 0 || report.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if _, routed := h.publisher.Hosts["www.example.com"]; routed {
		t.Fatal("a removed hostname was routed")
	}
}

// A project scheduled for deletion between listing and routing stays offline.
func TestAHostnameOfAProjectBeingDeletedIsNotRouted(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	h.projects.Stop(testProject)
	if got := h.refresh(t, site, added.ID); got.Status != sites.HostnameActive {
		t.Fatalf("status = %s", got.Status)
	}
	if _, routed := h.publisher.Hosts["www.example.com"]; routed {
		t.Fatal("routed while the project is scheduled for deletion")
	}
}

// A failed check is recorded too, so the sweep moves on and callers are throttled.
func TestAFailedCheckIsRecorded(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.FailGet = errors.New("down")
	*h.clock = h.clock.Add(time.Minute)
	if _, err := h.service.RefreshHostname(context.Background(), testProject, site.ID, added.ID); err == nil {
		t.Fatal("want the failure")
	}
	stored, _ := h.hostnames.GetHostname(context.Background(), testProject, site.ID, added.ID)
	if !stored.CheckedAt.Equal(*h.clock) || len(stored.Errors) == 0 {
		t.Fatalf("stored = %+v", stored)
	}
}

// A hostname deleted on Cloudflare by hand is created again on the next check.
func TestAHostnameGoneFromTheZoneIsCreatedAgain(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.Forget("www.example.com")
	h.refresh(t, site, added.ID)
	if !h.zone.Holds("www.example.com") || len(h.zone.Created()) != 2 {
		t.Fatalf("created = %v", h.zone.Created())
	}
}

// A domain that stopped serving releases its CORS origin, and one down for the whole lifetime is dropped.
func TestAHostnameDownForTheLifetimeIsDropped(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	h.refresh(t, site, added.ID)
	h.zone.SetState("www.example.com", "moved", "active")
	h.refresh(t, site, added.ID)
	if strings.Contains(strings.Join(h.origins.Origins, ","), "www.example.com") {
		t.Fatalf("origins = %v, want the custom origin released", h.origins.Origins)
	}
	*h.clock = h.clock.Add(sites.PendingHostnameLifetime + time.Hour)
	if report := h.service.RefreshPendingHostnames(context.Background()); report.Expired != 1 {
		t.Fatalf("report = %+v", report)
	}
	if h.zone.Holds("www.example.com") {
		t.Fatal("still on the zone")
	}
}
