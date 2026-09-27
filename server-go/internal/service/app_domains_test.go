package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const appHost = "storefront-deploy1.apps.example.com"

type fakeCNAMEs struct {
	mu      sync.Mutex
	targets map[string]string
	err     error
}

func (f *fakeCNAMEs) CNAME(_ context.Context, host string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.targets[host], f.err
}

func (f *fakeCNAMEs) point(host, target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets[host] = target
}

type fakeDomainStore struct {
	mu      sync.Mutex
	domains map[string]*apphost.Domain
	seq     int
}

func newFakeDomainStore() *fakeDomainStore {
	return &fakeDomainStore{domains: map[string]*apphost.Domain{}}
}

func (f *fakeDomainStore) Add(d *apphost.Domain) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	held := 0
	for _, existing := range f.domains {
		if existing.AppID == d.AppID {
			held++
			if existing.Hostname == d.Hostname {
				return apphost.ErrDomainExists
			}
		}
	}
	if held >= apphost.MaxDomainsPerApp {
		return apphost.ErrDomainLimit
	}
	copied := *d
	f.domains[d.ID] = &copied
	return nil
}

func (f *fakeDomainStore) List(projectID, appID string) ([]*apphost.Domain, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*apphost.Domain{}
	for _, d := range f.domains {
		if d.ProjectID == projectID && d.AppID == appID {
			copied := *d
			out = append(out, &copied)
		}
	}
	slices.SortFunc(out, func(a, b *apphost.Domain) int { return strings.Compare(a.Hostname, b.Hostname) })
	return out, nil
}

func (f *fakeDomainStore) Get(projectID, appID, id string) (*apphost.Domain, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.domains[id]
	if !ok || d.ProjectID != projectID || d.AppID != appID {
		return nil, nil
	}
	copied := *d
	return &copied, nil
}

func (f *fakeDomainStore) ListRoutable() ([]*apphost.Domain, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*apphost.Domain{}
	for _, d := range f.domains {
		if apphost.DomainRoutable(d.Status) {
			copied := *d
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (f *fakeDomainStore) Verify(projectID, appID, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.domains[id]
	if !ok || d.ProjectID != projectID || d.AppID != appID {
		return apphost.ErrDomainNotFound
	}
	for _, other := range f.domains {
		if other.ID != id && other.Hostname == d.Hostname && apphost.DomainRoutable(other.Status) {
			return apphost.ErrDomainClaimed
		}
	}
	d.Status, d.FailureReason, d.ConsecutiveFailures, d.VerifiedAt, d.LastCheckedAt = apphost.DomainIssuing, "", 0, &at, &at
	return nil
}

func (f *fakeDomainStore) SetStatus(id, status, reason string, failures int, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.domains[id]
	if !ok {
		return apphost.ErrDomainNotFound
	}
	d.Status, d.FailureReason, d.ConsecutiveFailures, d.LastCheckedAt = status, reason, failures, &at
	return nil
}

func (f *fakeDomainStore) Delete(projectID, appID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.domains[id]
	if !ok || d.ProjectID != projectID || d.AppID != appID {
		return apphost.ErrDomainNotFound
	}
	delete(f.domains, id)
	return nil
}

func (f *fakeDomainStore) status(id string) *apphost.Domain {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := *f.domains[id]
	return &copied
}

type domainFixture struct {
	svc     *AppDomainService
	store   *fakeDomainStore
	dns     *fakeCNAMEs
	kube    *k8s.MockClient
	app     *apphost.App
	now     time.Time
	deploys *AppDeployService
}

func newDomainFixture(t *testing.T) *domainFixture {
	t.Helper()
	lf := newLifecycleFixture(t, apphost.StatusRunning)
	store := newFakeDomainStore()
	dns := &fakeCNAMEs{targets: map[string]string{}}
	f := &domainFixture{store: store, dns: dns, kube: lf.kube, app: lf.app, now: time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC), deploys: lf.svc}
	f.svc = NewAppDomainService(lf.apps, store, lf.kube, lf.svc.instances, dns, lf.svc,
		apphost.Route{Domain: "apps.example.com", TLS: true}, k8s.AppDomainOptions{Issuer: "letsencrypt"})
	f.svc.now = func() time.Time { return f.now }
	return f
}

func (f *domainFixture) add(t *testing.T, host string) *apphost.Domain {
	t.Helper()
	view, err := f.svc.Add(context.Background(), f.app.ProjectID, f.app.ID, host)
	if err != nil {
		t.Fatalf("Add %s: %v", host, err)
	}
	return &view.Domain
}

func (f *domainFixture) hostsSynced() []string {
	return f.kube.DomainHosts[testDeployNamespace+"/"+f.app.ID]
}

func TestAddDomain_TellsWhereToPointTheCNAME(t *testing.T) {
	f := newDomainFixture(t)
	view, err := f.svc.Add(context.Background(), f.app.ProjectID, f.app.ID, "Shop.Example.com.")
	if err != nil {
		t.Fatal(err)
	}
	if view.Hostname != "shop.example.com" || view.Status != apphost.DomainPending || view.CNAMETarget != appHost {
		t.Fatalf("view = %+v", view)
	}
	if len(f.kube.Calls) != 0 {
		t.Fatal("nothing is routed before the CNAME is verified")
	}
	if _, err := f.svc.Add(context.Background(), f.app.ProjectID, f.app.ID, "example.com"); !errors.Is(err, apphost.ErrInvalidDomain) {
		t.Fatalf("apex: %v", err)
	}
	if _, err := f.svc.Add(context.Background(), f.app.ProjectID, "nope", "a.example.com"); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("missing app: %v", err)
	}
}

func TestVerifyDomain_RoutesOnlyACNAMEToThisAppsHostname(t *testing.T) {
	f := newDomainFixture(t)
	d := f.add(t, "shop.example.com")

	f.dns.point("shop.example.com", "someone-else.apps.example.com")
	if _, err := f.svc.Verify(context.Background(), f.app.ProjectID, f.app.ID, d.ID); !errors.Is(err, ErrDomainNotPointed) {
		t.Fatalf("wrong target: %v", err)
	}
	got := f.store.status(d.ID)
	if got.Status != apphost.DomainPending || !strings.Contains(got.FailureReason, "someone-else") {
		t.Fatalf("after a wrong CNAME = %+v", got)
	}
	if len(f.hostsSynced()) != 0 {
		t.Fatal("an unproven domain must not be routed")
	}

	f.dns.point("shop.example.com", appHost)
	view, err := f.svc.Verify(context.Background(), f.app.ProjectID, f.app.ID, d.ID)
	if err != nil || view.Status != apphost.DomainIssuing {
		t.Fatalf("Verify = %+v, %v", view, err)
	}
	if !slices.Equal(f.hostsSynced(), []string{"shop.example.com"}) {
		t.Fatalf("routed = %v", f.hostsSynced())
	}
}

func TestVerifyDomain_ALookupThatFailsIsNotANo(t *testing.T) {
	f := newDomainFixture(t)
	d := f.add(t, "shop.example.com")
	f.dns.err = errors.New("resolver timeout")
	if _, err := f.svc.Verify(context.Background(), f.app.ProjectID, f.app.ID, d.ID); err == nil || errors.Is(err, ErrDomainNotPointed) {
		t.Fatalf("err = %v", err)
	}
	if got := f.store.status(d.ID); got.Status != apphost.DomainPending {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestVerifyDomain_ARouteThatCannotBeWrittenStaysPending(t *testing.T) {
	f := newDomainFixture(t)
	d := f.add(t, "shop.example.com")
	f.dns.point("shop.example.com", appHost)
	f.kube.DomainSyncErr = errors.New("api down")
	if _, err := f.svc.Verify(context.Background(), f.app.ProjectID, f.app.ID, d.ID); err == nil {
		t.Fatal("want the error")
	}
	if got := f.store.status(d.ID); got.Status != apphost.DomainPending {
		t.Fatalf("status = %s, want pending so it is not held without a route", got.Status)
	}
}

func TestRemoveDomain_UnroutesIt(t *testing.T) {
	f := newDomainFixture(t)
	a, b := f.add(t, "a.example.com"), f.add(t, "b.example.com")
	for _, d := range []*apphost.Domain{a, b} {
		f.dns.point(d.Hostname, appHost)
		if _, err := f.svc.Verify(context.Background(), f.app.ProjectID, f.app.ID, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.Remove(context.Background(), f.app.ProjectID, f.app.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.hostsSynced(), []string{"b.example.com"}) {
		t.Fatalf("routed = %v", f.hostsSynced())
	}
	if err := f.svc.Remove(context.Background(), "proj-other", f.app.ID, b.ID); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("cross project: %v", err)
	}
}

func verified(t *testing.T, f *domainFixture, host string) *apphost.Domain {
	t.Helper()
	d := f.add(t, host)
	f.dns.point(host, appHost)
	if _, err := f.svc.Verify(context.Background(), f.app.ProjectID, f.app.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSweep_FollowsTheCertificate(t *testing.T) {
	f := newDomainFixture(t)
	d := verified(t, f, "shop.example.com")
	f.kube.DomainCerts["shop.example.com"] = k8s.CertificateState{Failure: "acme: rate limited"}
	f.svc.Sweep(context.Background())
	if got := f.store.status(d.ID); got.Status != apphost.DomainIssueFailed || got.FailureReason != "acme: rate limited" {
		t.Fatalf("after failure = %+v", got)
	}
	f.kube.DomainCerts["shop.example.com"] = k8s.CertificateState{Ready: true}
	f.kube.DomainHosts = map[string][]string{}
	f.svc.Sweep(context.Background())
	if got := f.store.status(d.ID); got.Status != apphost.DomainActive || got.FailureReason != "" {
		t.Fatalf("after issue = %+v", got)
	}
	if !slices.Equal(f.hostsSynced(), []string{"shop.example.com"}) {
		t.Fatal("an issued certificate must be put on the route")
	}
	f.kube.DomainCerts["shop.example.com"] = k8s.CertificateState{}
	f.svc.Sweep(context.Background())
	if got := f.store.status(d.ID); got.Status != apphost.DomainIssuing {
		t.Fatalf("a certificate being reissued, e.g. after a rename = %+v", got)
	}
}

// A CNAME that moved away is checked daily and the route goes after three failures in a row.
func TestSweep_DetachesADomainWhoseCNAMEMovedAway(t *testing.T) {
	f := newDomainFixture(t)
	d := verified(t, f, "shop.example.com")
	f.kube.DomainCerts["shop.example.com"] = k8s.CertificateState{Ready: true}
	f.dns.point("shop.example.com", "elsewhere.example.net")

	f.svc.Sweep(context.Background())
	if got := f.store.status(d.ID); got.ConsecutiveFailures != 0 {
		t.Fatalf("checked again before a day passed: %+v", got)
	}
	for day := 1; day <= apphost.DomainDetachAfter; day++ {
		f.now = f.now.Add(apphost.DomainRecheckInterval + time.Minute)
		f.svc.Sweep(context.Background())
		got := f.store.status(d.ID)
		if day < apphost.DomainDetachAfter && (got.ConsecutiveFailures != day || got.Status == apphost.DomainDetached) {
			t.Fatalf("day %d = %+v", day, got)
		}
	}
	got := f.store.status(d.ID)
	if got.Status != apphost.DomainDetached || !strings.Contains(got.FailureReason, "elsewhere") {
		t.Fatalf("after three failures = %+v", got)
	}
	if len(f.hostsSynced()) != 0 {
		t.Fatalf("still routed: %v", f.hostsSynced())
	}
}

func TestSweep_ACNAMEThatComesBackResetsTheCount(t *testing.T) {
	f := newDomainFixture(t)
	d := verified(t, f, "shop.example.com")
	f.kube.DomainCerts["shop.example.com"] = k8s.CertificateState{Ready: true}
	f.dns.point("shop.example.com", "elsewhere.example.net")
	f.now = f.now.Add(apphost.DomainRecheckInterval + time.Minute)
	f.svc.Sweep(context.Background())
	f.dns.point("shop.example.com", appHost)
	f.now = f.now.Add(apphost.DomainRecheckInterval + time.Minute)
	f.svc.Sweep(context.Background())
	if got := f.store.status(d.ID); got.ConsecutiveFailures != 0 || got.Status != apphost.DomainActive {
		t.Fatalf("got %+v", got)
	}
}

func TestListDomains_ShowTheTarget(t *testing.T) {
	f := newDomainFixture(t)
	f.add(t, "shop.example.com")
	views, err := f.svc.List(f.app.ProjectID, f.app.ID)
	if err != nil || len(views) != 1 || views[0].CNAMETarget != appHost {
		t.Fatalf("List = %+v, %v", views, err)
	}
}

func TestDeployApp_RoutesTheAppsDomainsUnderItsCurrentName(t *testing.T) {
	f := newDomainFixture(t)
	verified(t, f, "shop.example.com")
	f.add(t, "pending.example.com")
	f.kube.DomainHosts = map[string][]string{}
	f.deploys.SetDomainSync(f.svc.SyncApp)
	if _, err := f.deploys.DeployApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.hostsSynced(), []string{"shop.example.com"}) {
		t.Fatalf("routed = %v", f.hostsSynced())
	}
}

func TestStartDomainSweeper_SweepsWhileLeading(t *testing.T) {
	f := newDomainFixture(t)
	d := verified(t, f, "shop.example.com")
	f.kube.DomainCerts["shop.example.com"] = k8s.CertificateState{Ready: true}
	stop := f.svc.StartSweeper(context.Background(), NewLeadership(AlwaysLeader{}), time.Hour)
	defer stop()
	deadline := time.Now().Add(2 * time.Second)
	for f.store.status(d.ID).Status != apphost.DomainActive {
		if time.Now().After(deadline) {
			t.Fatal("the sweeper did not run at once")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSweep_AnAppThatWentIsSkipped(t *testing.T) {
	f := newDomainFixture(t)
	d := verified(t, f, "shop.example.com")
	f.svc.apps = newFakeAppStoreForDeploy(sampleDeployAppWithID("other"))
	f.svc.leases.apps = f.svc.apps
	f.svc.Sweep(context.Background())
	if got := f.store.status(d.ID); got.Status != apphost.DomainIssuing {
		t.Fatalf("status = %s", got.Status)
	}
}

func sampleDeployAppWithID(id string) *apphost.App {
	app := sampleDeployApp()
	app.ID = id
	return app
}

func TestVerifyDomain_EdgeCases(t *testing.T) {
	f := newDomainFixture(t)
	d := verified(t, f, "shop.example.com")
	if view, err := f.svc.Verify(context.Background(), f.app.ProjectID, f.app.ID, d.ID); err != nil || view.Status != apphost.DomainIssuing {
		t.Fatalf("verifying again = %+v, %v", view, err)
	}
	if _, err := f.svc.Verify(context.Background(), f.app.ProjectID, f.app.ID, "nope"); !errors.Is(err, apphost.ErrDomainNotFound) {
		t.Fatalf("missing: %v", err)
	}
	other := f.add(t, "www.example.com")
	f.store.domains["squat"] = &apphost.Domain{ID: "squat", ProjectID: "p2", AppID: "a2", Hostname: "www.example.com", Status: apphost.DomainActive}
	f.dns.point("www.example.com", appHost)
	if _, err := f.svc.Verify(context.Background(), f.app.ProjectID, f.app.ID, other.ID); !errors.Is(err, apphost.ErrDomainClaimed) {
		t.Fatalf("claimed: %v", err)
	}
	if err := f.svc.Remove(context.Background(), f.app.ProjectID, f.app.ID, "nope"); !errors.Is(err, apphost.ErrDomainNotFound) {
		t.Fatalf("remove missing: %v", err)
	}
	if _, err := f.svc.List(f.app.ProjectID, "nope"); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("list missing app: %v", err)
	}
}
