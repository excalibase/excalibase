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

const (
	testTarget   = "cname.excalibase.app"
	otherProject = "proj-other"
)

type domainHarness struct {
	*serviceHarness
	zone      *sitestest.Zone
	hostnames *sitestest.MemoryHostnames
	resolver  *sitestest.Resolver
	projects  *sitestest.Projects
	clock     *time.Time
}

func newDomainHarness(t *testing.T) *domainHarness {
	return newDomainHarnessWith(t, func(*sites.Options) {})
}

// newDomainHarnessWith lets a test change the service's options before it is built.
func newDomainHarnessWith(t *testing.T, configure func(*sites.Options)) *domainHarness {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins()
	h := &domainHarness{serviceHarness: &serviceHarness{store: store, publisher: publisher, origins: origins},
		zone: sitestest.NewZone(), hostnames: sitestest.NewMemoryHostnames(), resolver: sitestest.NewResolver(),
		projects: sitestest.NewProjects(), clock: &now}
	store.Now = func() time.Time { return *h.clock }
	options := sites.Options{
		BaseDomain: testDomain, SpoolDir: t.TempDir(), Hostnames: h.hostnames, Projects: h.projects,
		CustomDomains: &sites.CustomDomainOptions{Zone: h.zone, Target: testTarget, ReservedDomains: []string{"excalibase.io"},
			Resolver: h.resolver},
	}
	configure(&options)
	service, err := sites.NewService(store, publisher, origins, options)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.SetNow(func() time.Time { return *h.clock })
	h.service = service
	return h
}

// claim adds the domain without its proof: nothing reaches Cloudflare yet.
func (h *domainHarness) claim(t *testing.T, site *sites.Site, hostname string) *sites.CustomHostname {
	t.Helper()
	added, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: site.ProjectID, SiteID: site.ID, Hostname: hostname, Actor: testActor,
	})
	if err != nil {
		t.Fatalf("AddHostname(%s): %v", hostname, err)
	}
	return added
}

// add claims the domain, publishes its TXT proof and checks it, which creates it on the zone.
func (h *domainHarness) add(t *testing.T, site *sites.Site, hostname string) *sites.CustomHostname {
	t.Helper()
	added := h.claim(t, site, hostname)
	h.prove(t, added)
	return h.refresh(t, site, added.ID)
}

// prove publishes the TXT record the platform asked the owner for.
func (h *domainHarness) prove(t *testing.T, added *sites.CustomHostname) {
	t.Helper()
	for _, record := range added.Records {
		if record.Purpose == sites.PurposeVerification {
			h.resolver.Add(record.Name, record.Value)
			return
		}
	}
	t.Fatalf("no verification record in %+v", added.Records)
}

func (h *domainHarness) refresh(t *testing.T, site *sites.Site, id string) *sites.CustomHostname {
	t.Helper()
	refreshed, err := h.tryRefresh(site, id)
	if err != nil {
		t.Fatalf("RefreshHostname: %v", err)
	}
	return refreshed
}

func (h *domainHarness) tryRefresh(site *sites.Site, id string) (*sites.CustomHostname, error) {
	*h.clock = h.clock.Add(time.Minute)
	return h.service.RefreshHostname(context.Background(), site.ProjectID, site.ID, id)
}

func (h *domainHarness) stored(t *testing.T, site *sites.Site, id string) *sites.CustomHostname {
	t.Helper()
	stored, err := h.hostnames.GetHostname(context.Background(), site.ProjectID, site.ID, id)
	if err != nil || stored == nil {
		t.Fatalf("stored hostname %s: %v %v", id, stored, err)
	}
	return stored
}

func (h *domainHarness) liveSite(t *testing.T, name string) *sites.Site {
	t.Helper()
	site := h.createSite(t, name)
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	return site
}

// otherSite is a site of another project, stored directly.
func (h *domainHarness) otherSite(t *testing.T, project, name string) *sites.Site {
	t.Helper()
	site := &sites.Site{ID: "site-" + project + "-" + name, ProjectID: project, Name: name, Hostname: name + "." + testDomain}
	if err := h.store.Create(context.Background(), site, 5, 0); err != nil {
		t.Fatal(err)
	}
	return site
}

func (h *domainHarness) routed(hostname string) bool {
	_, ok := h.publisher.Hosts[hostname]
	return ok
}

func recordKinds(records []sites.DNSInstruction) string {
	kinds := []string{}
	for _, record := range records {
		kinds = append(kinds, record.Type+":"+record.Purpose)
	}
	return strings.Join(kinds, ",")
}

// Adding only claims the name: no Cloudflare call until whoever added it proves control.
func TestAddingADomainAsksForItsRecordsAndCallsNoCloudflare(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.claim(t, site, "WWW.Example.com")
	if added.Hostname != "www.example.com" || added.Status != sites.HostnamePending || added.Validation != "http" || added.VerifiedAt != nil {
		t.Fatalf("added = %+v", added)
	}
	if len(h.zone.Created()) != 0 || h.zone.Reads() != 0 {
		t.Fatalf("cloudflare was called for an unproven claim: %v", h.zone.Created())
	}
	if recordKinds(added.Records) != "CNAME:routing,TXT:verification" || added.Records[0].Value != testTarget {
		t.Fatalf("records = %+v", added.Records)
	}
	if verify := added.Records[1]; verify.Name != "_excalibase-challenge.www.example.com" || !strings.HasPrefix(verify.Value, "excalibase-verification=") {
		t.Fatalf("verification record = %+v", verify)
	}
	if h.routed("www.example.com") {
		t.Fatal("an unproven claim was written to the hosts table")
	}
}

func TestACheckWithoutTheProofCallsNoCloudflare(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.claim(t, site, "www.example.com")
	h.resolver.Set("_excalibase-challenge.www.example.com", "excalibase-verification=someone-elses")
	got := h.refresh(t, site, added.ID)
	if got.VerifiedAt != nil || got.Status != sites.HostnamePending || len(h.zone.Created()) != 0 || h.zone.Reads() != 0 {
		t.Fatalf("got = %+v created = %v", got, h.zone.Created())
	}
	if !strings.Contains(strings.Join(got.Errors, " "), "_excalibase-challenge.www.example.com") {
		t.Fatalf("errors = %v, want the TXT record named", got.Errors)
	}
}

func TestTheProofCreatesTheHostnameOnTheZone(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	if added.VerifiedAt == nil || added.Status != sites.HostnamePending {
		t.Fatalf("added = %+v", added)
	}
	if created := h.zone.Created(); len(created) != 1 || created[0] != "www.example.com/http" {
		t.Fatalf("cloudflare got %v", created)
	}
	// The proof is checked again while the domain serves, so its record stays listed.
	if recordKinds(added.Records) != "CNAME:routing,TXT:verification" {
		t.Fatalf("records = %+v", added.Records)
	}
}

func TestTXTValidationShowsCloudflaresRecordsOnceProven(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "www.example.com", Validation: "txt", Actor: testActor,
	})
	if err != nil {
		t.Fatalf("AddHostname: %v", err)
	}
	h.prove(t, added)
	got := h.refresh(t, site, added.ID)
	if recordKinds(got.Records) != "CNAME:routing,TXT:verification,TXT:ownership,TXT:certificate" {
		t.Fatalf("records = %v", recordKinds(got.Records))
	}
	if h.zone.Created()[0] != "www.example.com/txt" {
		t.Fatalf("cloudflare got %v", h.zone.Created())
	}
}

func TestAddHostnameRefusesBadInput(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 36) + ".com"
	for _, request := range []sites.AddHostnameRequest{
		{Hostname: "1.2.3.4"}, {Hostname: "other.excalibase.app"}, {Hostname: "www.excalibase.io"},
		{Hostname: "example.com"}, {Hostname: "www.example.com", Validation: "email"}, {Hostname: long},
	} {
		request.ProjectID, request.SiteID, request.Actor = testProject, site.ID, testActor
		_, err := h.service.AddHostname(context.Background(), request)
		var inputErr *sites.InputError
		if !errors.As(err, &inputErr) {
			t.Errorf("%+v: err = %v, want an input refusal", request, err)
		}
	}
}

// An unproven claim holds nothing: whoever proves control first gets the domain.
func TestAnUnprovenClaimHoldsNothing(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	squatter := h.claim(t, site, "www.example.com")
	if _, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "www.example.com", Actor: testActor,
	}); !errors.Is(err, sites.ErrHostnameTaken) {
		t.Fatalf("err = %v, want one claim per project", err)
	}
	owner := h.otherSite(t, otherProject, "owner")
	proven := h.add(t, owner, "www.example.com")
	if proven.VerifiedAt == nil {
		t.Fatalf("the owner's proof was not taken: %+v", proven)
	}
	h.prove(t, squatter)
	if _, err := h.tryRefresh(site, squatter.ID); !errors.Is(err, sites.ErrHostnameTaken) {
		t.Fatalf("err = %v, want the name held by the proven claim", err)
	}
	if len(h.zone.Created()) != 1 {
		t.Fatalf("cloudflare got %v", h.zone.Created())
	}
	late := h.otherSite(t, "proj-late", "late")
	if _, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: late.ProjectID, SiteID: late.ID, Hostname: "www.example.com", Actor: testActor,
	}); !errors.Is(err, sites.ErrHostnameTaken) {
		t.Fatalf("err = %v, want a proven name refused to everyone else", err)
	}
}

// The limit counts claims waiting for their proof too.
func TestAProjectHoldsAtMostThePlansLimitAcrossItsSites(t *testing.T) {
	h := newDomainHarness(t)
	shop, blog := h.liveSite(t, "shop"), h.liveSite(t, "journal")
	h.claim(t, shop, "www.example.com")
	h.claim(t, blog, "blog.example.com")
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
	h := newDomainHarnessWith(t, func(options *sites.Options) { options.DeployLimits = fixedLimits{limits} })
	shop := h.liveSite(t, "shop")
	for _, host := range []string{"a.example.com", "b.example.com", "c.example.com"} {
		h.claim(t, shop, host)
	}
	if _, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: shop.ID, Hostname: "d.example.com", Actor: testActor,
	}); !errors.Is(err, sites.ErrHostnameLimitReached) || !strings.Contains(err.Error(), "at most 3") {
		t.Fatalf("err = %v, want the plan's limit of 3", err)
	}
	limits.MaxCustomDomains = 0
	none := newDomainHarnessWith(t, func(options *sites.Options) { options.DeployLimits = fixedLimits{limits} })
	site := none.liveSite(t, "shop")
	if _, err := none.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "a.example.com", Actor: testActor,
	}); !errors.Is(err, sites.ErrHostnameLimitReached) {
		t.Fatalf("err = %v, want a plan without custom domains refused", err)
	}
}

// A create that reached Cloudflare but whose answer was lost is adopted, not refused.
func TestTheProofAdoptsTheZonesOwnCopyOfAHostnameItHolds(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	existing := h.zone.Seed("www.example.com", "pending", "pending_validation")
	added := h.add(t, site, "www.example.com")
	if stored := h.hostnames.CloudflareID(added.ID); stored != existing {
		t.Fatalf("cloudflare id = %q, want the adopted %q", stored, existing)
	}
}

func TestRefreshRoutesTheHostnameOnceItAndItsCertificateAreActive(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "pending_validation")
	if got := h.refresh(t, site, added.ID); got.Status != sites.HostnamePending {
		t.Fatalf("status = %s with the certificate still pending", got.Status)
	}
	if h.routed("www.example.com") {
		t.Fatal("routed before the certificate was active")
	}
	if h.zone.Revalidations() < 1 {
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
	if h.routed("www.example.com") {
		t.Fatal("routed to a Worker that does not exist yet")
	}
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if h.publisher.Hosts["www.example.com"] != sites.ScriptName(site) {
		t.Fatalf("hosts = %v, want the deploy to route the active hostname", h.publisher.Hosts)
	}
}

func TestRefreshWithinSecondsAnswersTheStoredStateWithoutCallingCloudflare(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	reads, lookups := h.zone.Reads(), len(h.resolver.Lookups())
	if _, err := h.service.RefreshHostname(context.Background(), testProject, site.ID, added.ID); err != nil {
		t.Fatalf("RefreshHostname: %v", err)
	}
	if h.zone.Reads() != reads || len(h.resolver.Lookups()) != lookups {
		t.Fatal("a refresh right after the last check looked again")
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
	if _, err := h.tryRefresh(site, added.ID); !errors.Is(err, sites.ErrHostnameNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// Removing an unproven claim touches neither the router nor the zone: the name there may be someone else's.
func TestRemovingAnUnprovenClaimLeavesTheProvenOwnerServing(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	squatter := h.claim(t, site, "www.example.com")
	owner := h.otherSite(t, otherProject, "owner")
	proven := h.add(t, owner, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	h.publisher.Hosts["www.example.com"] = sites.ScriptName(owner)
	h.refresh(t, owner, proven.ID)
	h.publisher.Calls, h.zone.Log = nil, nil
	if err := h.service.RemoveHostname(context.Background(), testProject, site.ID, squatter.ID); err != nil {
		t.Fatalf("RemoveHostname: %v", err)
	}
	if len(h.publisher.Calls) != 0 || len(h.zone.Log) != 0 || !h.zone.Holds("www.example.com") || !h.routed("www.example.com") {
		t.Fatalf("calls = %v zone = %v", h.publisher.Calls, h.zone.Log)
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
	h.claim(t, site, "waiting.example.com")
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
	// The cancel runs while the project still reads as scheduled for deletion.
	h.projects.Stop(testProject)
	*h.clock = h.clock.Add(time.Minute)
	if err := h.service.RouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(h.publisher.Hosts) != 2 || h.publisher.Hosts["www.example.com"] != sites.ScriptName(site) {
		t.Fatalf("hosts = %v, want the site and its active hostname only", h.publisher.Hosts)
	}
}

// A project's grace period leaves alone a name another project proved: its own claim on it is unproven.
func TestGracePeriodLeavesAnotherProjectsProvenNameRouted(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	h.claim(t, site, "www.example.com")
	owner := h.otherSite(t, otherProject, "owner")
	h.add(t, owner, "www.example.com")
	h.publisher.Hosts["www.example.com"] = sites.ScriptName(owner)
	if err := h.service.UnrouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("Unroute: %v", err)
	}
	if h.publisher.Hosts["www.example.com"] != sites.ScriptName(owner) {
		t.Fatalf("hosts = %v, want the owner still routed", h.publisher.Hosts)
	}
}

func TestTheSweepChecksWaitingHostnamesAndDropsAbandonedOnes(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	h.add(t, site, "www.example.com")
	h.claim(t, site, "old.example.com")
	h.hostnames.Age("old.example.com", sites.PendingHostnameLifetime+time.Hour)
	h.zone.SetState("www.example.com", "active", "active")
	*h.clock = h.clock.Add(time.Minute)
	report := h.service.RefreshPendingHostnames(context.Background())
	if report.Activated != 1 || report.Expired != 1 || report.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if h.publisher.Hosts["www.example.com"] != sites.ScriptName(site) {
		t.Fatalf("hosts = %v", h.publisher.Hosts)
	}
	if listed, _ := h.hostnames.ListProjectHostnames(context.Background(), testProject); len(listed) != 1 {
		t.Fatalf("hostnames = %+v, want the abandoned claim dropped", listed)
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

// A create whose answer was lost may have reached the zone: the proof is kept and the next check adopts it.
func TestAnUnansweredCreateIsAdoptedOnTheNextCheck(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.claim(t, site, "www.example.com")
	h.prove(t, added)
	h.zone.FailCreateAfterStore = context.DeadlineExceeded
	if _, err := h.tryRefresh(site, added.ID); err == nil {
		t.Fatal("want the failure")
	}
	if stored := h.stored(t, site, added.ID); stored.VerifiedAt == nil || len(stored.Errors) == 0 {
		t.Fatalf("stored = %+v, want the proof kept and the failure shown", stored)
	}
	h.zone.FailCreateAfterStore = nil
	got := h.refresh(t, site, added.ID)
	if h.hostnames.CloudflareID(got.ID) == "" || len(h.zone.Created()) != 0 {
		t.Fatalf("the zone's copy was not adopted: id=%q created=%v", h.hostnames.CloudflareID(got.ID), h.zone.Created())
	}
}

// A definite refusal created nothing: the claim stays, unproven, with Cloudflare's reason.
func TestADefiniteRefusalReleasesTheProof(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.claim(t, site, "www.example.com")
	h.prove(t, added)
	h.zone.FailCreate = &cfworkers.APIError{Status: 400, Codes: []int{1411}, Message: "hostname not allowed"}
	if _, err := h.tryRefresh(site, added.ID); err == nil {
		t.Fatal("want the refusal")
	}
	if stored := h.stored(t, site, added.ID); stored.VerifiedAt != nil || len(stored.Errors) == 0 {
		t.Fatalf("stored = %+v, want the name released and the reason shown", stored)
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
	if h.routed("www.example.com") {
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
	if h.routed("www.example.com") {
		t.Fatal("routed while the project is scheduled for deletion")
	}
}

// A failed check is recorded too, so the sweep moves on and callers are throttled.
func TestAFailedCheckIsRecorded(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.FailGet = errors.New("down")
	if _, err := h.tryRefresh(site, added.ID); err == nil {
		t.Fatal("want the failure")
	}
	if stored := h.stored(t, site, added.ID); !stored.CheckedAt.Equal(*h.clock) || len(stored.Errors) == 0 {
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

// A domain not serving for the whole lifetime is dropped, from the zone too.
func TestAHostnameDownForTheLifetimeIsDropped(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	h.add(t, site, "www.example.com")
	*h.clock = h.clock.Add(sites.PendingHostnameLifetime + time.Hour)
	if report := h.service.RefreshPendingHostnames(context.Background()); report.Expired != 1 {
		t.Fatalf("report = %+v", report)
	}
	if h.zone.Holds("www.example.com") {
		t.Fatal("still on the zone")
	}
}
