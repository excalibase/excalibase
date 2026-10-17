package sites_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// activeDomain is a proven domain serving the live site.
func activeDomain(t *testing.T, h *domainHarness) (*sites.Site, *sites.CustomHostname) {
	t.Helper()
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	got := h.refresh(t, site, added.ID)
	if got.Status != sites.HostnameActive || !h.routed("www.example.com") {
		t.Fatalf("setup: %+v hosts %v", got, h.publisher.Hosts)
	}
	return site, got
}

func assertWithdrawn(t *testing.T, h *domainHarness, site *sites.Site, id string) {
	t.Helper()
	stored := h.stored(t, site, id)
	if stored.VerifiedAt != nil || stored.Status == sites.HostnameActive || h.hostnames.CloudflareID(id) != "" {
		t.Fatalf("stored = %+v, want the proof cleared and the hostname off the zone", stored)
	}
	if h.routed("www.example.com") || h.zone.Holds("www.example.com") {
		t.Fatalf("hosts = %v zone holds = %v", h.publisher.Hosts, h.zone.Holds("www.example.com"))
	}
	if strings.Contains(strings.Join(h.origins.Origins, ","), "www.example.com") {
		t.Fatalf("origins = %v, want the custom origin released", h.origins.Origins)
	}
}

// F2: an active domain is checked again, on a slower cadence, and leaves the
// router and the zone once its owner's TXT record is gone.
func TestTheSweepRechecksActiveDomainsAndWithdrawsOneWhoseProofIsGone(t *testing.T) {
	h := newDomainHarness(t)
	site, active := activeDomain(t, h)
	h.resolver.Remove("_excalibase-challenge.www.example.com")
	reads := h.zone.Reads()
	*h.clock = h.clock.Add(10 * time.Minute)
	h.service.RefreshPendingHostnames(context.Background())
	if h.zone.Reads() != reads || !h.routed("www.example.com") {
		t.Fatal("an active domain was checked again within the hour")
	}
	*h.clock = h.clock.Add(sites.ActiveHostnameRecheckInterval)
	h.service.RefreshPendingHostnames(context.Background())
	assertWithdrawn(t, h, site, active.ID)
}

func TestAnActiveDomainCloudflareCallsMovedIsWithdrawnAndProvenAgain(t *testing.T) {
	h := newDomainHarness(t)
	site, active := activeDomain(t, h)
	h.zone.SetState("www.example.com", "moved", "active")
	h.refresh(t, site, active.ID)
	assertWithdrawn(t, h, site, active.ID)
	// The owner's record is still there, but a withdrawn domain waits out a cooldown first:
	// an answer that flips every few seconds cannot make Cloudflare re-issue a certificate each time.
	waiting := h.refresh(t, site, active.ID)
	if waiting.VerifiedAt != nil || len(h.zone.Created()) != 1 {
		t.Fatalf("proven again within the cooldown: %+v created = %v", waiting, h.zone.Created())
	}
	*h.clock = h.clock.Add(sites.WithdrawnHostnameCooldown)
	again := h.refresh(t, site, active.ID)
	if again.VerifiedAt == nil || !h.zone.Holds("www.example.com") || len(h.zone.Created()) != 2 {
		t.Fatalf("again = %+v created = %v", again, h.zone.Created())
	}
}

// The GET decides: a moved domain is withdrawn even when the re-validate PATCH fails.
func TestAMovedDomainIsWithdrawnEvenWhenTheRevalidateFails(t *testing.T) {
	h := newDomainHarness(t)
	site, active := activeDomain(t, h)
	h.zone.SetState("www.example.com", "moved", "active")
	h.zone.FailRevalidate = errors.New("cloudflare down")
	h.refresh(t, site, active.ID)
	assertWithdrawn(t, h, site, active.ID)
}

// A deploy refused because the project is busy publishes nothing, so it does
// not use up the hour's deploy: on a 1-per-hour plan the retry still goes out.
func TestABusyRefusalGivesTheDeploySlotBack(t *testing.T) {
	limits := sites.DefaultLimits
	limits.MaxDeploysPerHour = 1
	h := newDomainHarnessWith(t, func(options *sites.Options) { options.DeployLimits = fixedLimits{limits} })
	site := h.liveSite(t, "shop")
	*h.clock = h.clock.Add(time.Hour + time.Minute)
	h.projects.Busy(testProject, true)
	if _, err := h.deploy(t, site, viteDist(t)); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("deploy while busy: err = %v, want the project busy", err)
	}
	h.projects.Busy(testProject, false)
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := h.deploy(t, site, viteDist(t)); !errors.Is(err, sites.ErrDeployRateLimited) {
		t.Fatalf("third deploy: err = %v, want the hourly rate", err)
	}
}

// A blocked, moved or deleted GET decides even when a PATCH would answer
// active: the PATCH is only for a hostname still pending validation.
func TestAFailedGetDecidesOverARevalidateThatAnswersActive(t *testing.T) {
	for _, state := range []string{"blocked", "moved", "deleted"} {
		h := newDomainHarness(t)
		site, active := activeDomain(t, h)
		h.zone.SetState("www.example.com", state, "active")
		h.zone.RevalidateAnswer = &cfworkers.CustomHostname{ID: active.CloudflareID, Hostname: "www.example.com",
			Status: "active", SSL: cfworkers.HostnameSSL{Status: "active"}}
		before := h.zone.Revalidations()
		h.refresh(t, site, active.ID)
		assertWithdrawn(t, h, site, active.ID)
		if h.zone.Revalidations() != before {
			t.Fatalf("%s: re-validated %d times, want no PATCH", state, h.zone.Revalidations()-before)
		}
	}
}

// A re-create Cloudflare refuses outright releases the proof, and so leaves the router and CORS as a withdraw does.
func TestARefusedRecreateTakesTheDomainOffLikeAWithdraw(t *testing.T) {
	h := newDomainHarness(t)
	site, active := activeDomain(t, h)
	h.zone.Forget("www.example.com")
	h.zone.FailCreate = &cfworkers.APIError{Status: 400, Codes: []int{1411}, Message: "hostname not allowed"}
	if _, err := h.tryRefresh(site, active.ID); err == nil {
		t.Fatal("want the refusal")
	}
	assertWithdrawn(t, h, site, active.ID)
	if stored := h.stored(t, site, active.ID); !strings.Contains(strings.Join(stored.Errors, " "), "hostname not allowed") {
		t.Fatalf("errors = %v", stored.Errors)
	}
}

// A rate limit or a Cloudflare outage on a re-create is transient: the proof, the route and the origin stay.
func TestATransientRecreateFailureKeepsTheDomainServing(t *testing.T) {
	for _, status := range []int{429, 503} {
		h := newDomainHarness(t)
		site, active := activeDomain(t, h)
		h.zone.Forget("www.example.com")
		h.zone.FailCreate = &cfworkers.APIError{Status: status, Message: "try later"}
		if _, err := h.tryRefresh(site, active.ID); err == nil {
			t.Fatalf("%d: want the failure", status)
		}
		stored := h.stored(t, site, active.ID)
		if stored.VerifiedAt == nil || stored.Status != sites.HostnameActive || !h.routed("www.example.com") {
			t.Fatalf("%d: stored = %+v hosts = %v", status, stored, h.publisher.Hosts)
		}
	}
}

func TestAnActiveDomainWhoseCertificateLapsedIsWithdrawn(t *testing.T) {
	h := newDomainHarness(t)
	site, active := activeDomain(t, h)
	h.zone.SetState("www.example.com", "active", "expired")
	h.refresh(t, site, active.ID)
	assertWithdrawn(t, h, site, active.ID)
}

// A lookup that fails for another reason than a missing record proves nothing either way.
func TestATransientDNSFailureKeepsAnActiveDomainServing(t *testing.T) {
	h := newDomainHarness(t)
	site, active := activeDomain(t, h)
	h.resolver.FailWith(&net.DNSError{Err: "server misbehaving", Name: "x", IsTemporary: true})
	got := h.refresh(t, site, active.ID)
	if got.Status != sites.HostnameActive || got.VerifiedAt == nil || !h.routed("www.example.com") {
		t.Fatalf("got = %+v hosts = %v", got, h.publisher.Hosts)
	}
}

// F2: routing reads Cloudflare again instead of trusting what was stored.
func TestADeployRefreshesCustomDomainsBeforeRoutingThem(t *testing.T) {
	h := newDomainHarness(t)
	site, active := activeDomain(t, h)
	h.zone.SetState("www.example.com", "moved", "active")
	delete(h.publisher.Hosts, "www.example.com")
	*h.clock = h.clock.Add(time.Minute)
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if h.routed("www.example.com") {
		t.Fatal("a deploy routed a domain Cloudflare no longer serves")
	}
	if stored := h.stored(t, site, active.ID); stored.VerifiedAt != nil {
		t.Fatalf("stored = %+v, want it withdrawn", stored)
	}
}

func TestACancelledDeletionRefreshesCustomDomainsBeforeRoutingThem(t *testing.T) {
	h := newDomainHarness(t)
	_, _ = activeDomain(t, h)
	if err := h.service.UnrouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("Unroute: %v", err)
	}
	h.resolver.Remove("_excalibase-challenge.www.example.com")
	*h.clock = h.clock.Add(time.Minute)
	if err := h.service.RouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if h.routed("www.example.com") || h.zone.Holds("www.example.com") {
		t.Fatalf("hosts = %v, want the unproven domain left off", h.publisher.Hosts)
	}
}

// F3: the zone holds at most the install's cap, under the hostnames Cloudflare includes.
func TestTheZoneCapRefusesANewClaimOnceTheZoneIsFull(t *testing.T) {
	h := newDomainHarnessWith(t, func(options *sites.Options) { options.CustomDomains.MaxOnZone = 1 })
	site := h.liveSite(t, "shop")
	h.add(t, site, "www.example.com")
	_, err := h.service.AddHostname(context.Background(), sites.AddHostnameRequest{
		ProjectID: testProject, SiteID: site.ID, Hostname: "blog.example.com", Actor: testActor,
	})
	if !errors.Is(err, sites.ErrCustomDomainsFull) {
		t.Fatalf("err = %v, want the zone full", err)
	}
}

func TestTheZoneCapRefusesAProofOnceTheZoneFilledMeanwhile(t *testing.T) {
	h := newDomainHarnessWith(t, func(options *sites.Options) { options.CustomDomains.MaxOnZone = 1 })
	site := h.liveSite(t, "shop")
	first, second := h.claim(t, site, "www.example.com"), h.claim(t, site, "blog.example.com")
	h.prove(t, first)
	h.prove(t, second)
	h.refresh(t, site, first.ID)
	if _, err := h.tryRefresh(site, second.ID); !errors.Is(err, sites.ErrCustomDomainsFull) {
		t.Fatalf("err = %v, want the zone full", err)
	}
	if h.zone.Holds("blog.example.com") || h.stored(t, site, second.ID).VerifiedAt != nil {
		t.Fatal("a proof past the cap reached the zone")
	}
}

func TestTheZoneCapDefaultsUnderTheHundredCloudflareIncludes(t *testing.T) {
	if sites.DefaultMaxZoneHostnames != 90 {
		t.Fatalf("default = %d", sites.DefaultMaxZoneHostnames)
	}
}

// F4: checks take the project's lifecycle lease, so a deletion's unroute cannot interleave with a route.
func TestARefreshTakesTheProjectsLifecycleLease(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	added := h.add(t, site, "www.example.com")
	if h.projects.Holds() == 0 {
		t.Fatal("the check did not take the project's lifecycle lease")
	}
	h.projects.Busy(testProject, true)
	reads := h.zone.Reads()
	if _, err := h.tryRefresh(site, added.ID); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("err = %v, want busy while another lifecycle operation runs", err)
	}
	*h.clock = h.clock.Add(time.Hour)
	if report := h.service.RefreshPendingHostnames(context.Background()); report.Checked != 0 || h.zone.Reads() != reads {
		t.Fatalf("report = %+v, want the sweep to skip the project", report)
	}
}

// In the deletion's real order: holding the lifecycle lease, it unroutes the
// project's sites, then writes PENDING_DELETION. A deploy or a rollback between
// the two is refused and routes nothing, base host included; once the status
// is written, neither can route the project again.
func TestADeployOrRollbackDuringADeletionRoutesNothing(t *testing.T) {
	h := newDomainHarness(t)
	site, _ := activeDomain(t, h)
	first, err := h.deploy(t, site, viteDist(t))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	h.projects.Busy(testProject, true)
	if err := h.service.UnrouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("Unroute: %v", err)
	}
	h.publisher.Calls = nil
	*h.clock = h.clock.Add(time.Minute)
	if _, err := h.deploy(t, site, viteDist(t)); !errors.Is(err, sites.ErrSiteBusy) || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("deploy during the unroute: err = %v, want the project busy", err)
	}
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, first.ID); !errors.Is(err, sites.ErrSiteBusy) {
		t.Fatalf("rollback during the unroute: err = %v, want the project busy", err)
	}
	h.projects.Stop(testProject)
	h.projects.Busy(testProject, false)
	if _, err := h.deploy(t, site, viteDist(t)); !errors.Is(err, sites.ErrProjectNotServing) {
		t.Fatalf("deploy once scheduled: err = %v, want refused", err)
	}
	if _, err := h.service.Rollback(context.Background(), testProject, site.ID, first.ID); !errors.Is(err, sites.ErrProjectNotServing) {
		t.Fatalf("rollback once scheduled: err = %v, want refused", err)
	}
	for _, call := range h.publisher.Calls {
		if call == "put-host" {
			t.Fatalf("calls = %v, want no route written", h.publisher.Calls)
		}
	}
	if len(h.publisher.Hosts) != 0 {
		t.Fatalf("hosts = %v, want the project left off for the grace period", h.publisher.Hosts)
	}
}

// F5: the proof is looked up as a fully qualified name, so no search domain is appended.
func TestTheProofIsLookedUpFullyQualified(t *testing.T) {
	h := newDomainHarness(t)
	site := h.liveSite(t, "shop")
	h.add(t, site, "www.example.com")
	lookups := h.resolver.Lookups()
	if len(lookups) == 0 {
		t.Fatal("no lookup")
	}
	for _, name := range lookups {
		if name != "_excalibase-challenge.www.example.com." {
			t.Fatalf("looked up %q", name)
		}
	}
}
