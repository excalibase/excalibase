package dockerapps

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func TestAppLogsReadTheContainersInTimeOrder(t *testing.T) {
	h := newHarness(t, nil)
	app := freeApp()
	app.Tier, app.Replicas = "STANDARD", 2
	h.rollout(app, "dep-1")
	apps := h.engine.appContainers("app-01")
	apps[0].logs = "2026-10-10T01:00:01.000000000Z first\n2026-10-10T01:00:03.000000000Z third\n"
	apps[1].logs = "2026-10-10T01:00:02.000000000Z second\n"
	page, err := h.rt.AppLogs(context.Background(), testDB, "app-01", k8s.AppLogOptions{TailLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, line := range page.Lines {
		texts = append(texts, line.Text)
	}
	if strings.Join(texts, ",") != "first,second,third" || page.Lines[1].Pod != apps[1].name {
		t.Fatalf("lines %+v", page.Lines)
	}
	since := time.Date(2026, 10, 10, 1, 0, 2, 0, time.UTC)
	page, err = h.rt.AppLogs(context.Background(), testDB, "app-01", k8s.AppLogOptions{Since: &since})
	if err != nil || len(page.Lines) != 1 || page.Lines[0].Text != "third" {
		t.Fatalf("since: %+v (%v)", page.Lines, err)
	}
}

func TestAppLogsOfAnotherProjectsAppAreEmpty(t *testing.T) {
	h := newHarness(t, nil)
	h.rollout(freeApp(), "dep-1")
	h.engine.appContainers("app-01")[0].logs = "2026-10-10T01:00:01.000000000Z secret\n"
	h.rt.opts.Projects = fakeScopes{testDB: testProject, "db-other": "proj-other"}
	page, err := h.rt.AppLogs(context.Background(), "db-other", "app-01", k8s.AppLogOptions{TailLines: 10})
	if err != nil || len(page.Lines) != 0 {
		t.Fatalf("read another project's logs: %+v (%v)", page.Lines, err)
	}
}

// edgeAt serves TLS with httptest's certificate (example.com, *.example.com),
// only to the server names in hosts, and makes the runtime trust its issuer.
func edgeAt(t *testing.T, h *harness, hosts ...string) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	cert := server.TLS.Certificates[0]
	server.TLS.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if slices.Contains(hosts, hello.ServerName) {
			return &cert, nil
		}
		return nil, errors.New("no certificate for " + hello.ServerName)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	h.rt.opts.EdgeTLSAddress, h.rt.opts.EdgeRoots = server.Listener.Addr().String(), roots
}

func TestHostCertificateIsReadyOnlyWhenTheEdgeServesAValidOne(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Route.Domain = "example.com" })
	h.rollout(freeApp(), "dep-1")
	ctx := context.Background()
	edgeAt(t, h, "web-abc.example.com")
	if state, err := h.rt.AppHostCertificate(ctx, testDB, "web"); err != nil || !state.Ready {
		t.Fatalf("a valid certificate for the host: %+v (%v)", state, err)
	}
	edgeAt(t, h, "nothing.example.com")
	if state, err := h.rt.AppHostCertificate(ctx, testDB, "web"); err != nil || state.Ready || state.Failure == "" {
		t.Fatalf("no certificate served: %+v (%v)", state, err)
	}
	if _, err := h.rt.AppHostCertificate(ctx, testDB, "nope"); !errors.Is(err, k8s.ErrNoCertificate) {
		t.Fatalf("unknown app: %v", err)
	}
}

func TestACertificateNotNamingTheHostOrUntrustedIsNotReady(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	edgeAt(t, h, "web-abc.apps.example.com")
	if state, _ := h.rt.certificateFor(ctx, "web-abc.apps.example.com"); state.Ready {
		t.Fatal("a certificate for *.example.com counted for web-abc.apps.example.com")
	}
	edgeAt(t, h, "shop.example.com")
	h.rt.opts.EdgeRoots = x509.NewCertPool()
	if state, _ := h.rt.certificateFor(ctx, "shop.example.com"); state.Ready {
		t.Fatal("a certificate from an untrusted issuer counted")
	}
}

// With its own CA the edge issues on the spot and cannot fail: a routed host is ready.
func TestTheEdgesOwnCAIsReadyOnceTheHostIsRouted(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.EdgeIssuesLocally = true })
	ctx := context.Background()
	app := freeApp()
	h.rollout(app, "dep-1")
	if state, err := h.rt.AppHostCertificate(ctx, testDB, "web"); err != nil || !state.Ready {
		t.Fatalf("host: %+v (%v)", state, err)
	}
	if state, _ := h.rt.AppDomainCertificate(ctx, testDB, "web", "shop.example.org"); state.Ready {
		t.Fatal("a domain not routed yet counted as issued")
	}
	if err := h.rt.SyncAppDomains(ctx, testDB, app, []string{"shop.example.org"}, k8s.AppDomainOptions{}); err != nil {
		t.Fatal(err)
	}
	if state, err := h.rt.AppDomainCertificate(ctx, testDB, "web", "shop.example.org"); err != nil || !state.Ready {
		t.Fatalf("routed domain: %+v (%v)", state, err)
	}
}

func TestCapacityCountsAppRequestsAndDatabaseLimits(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.containers[testDB].host.NanoCPUs = 1_000_000_000
	h.engine.containers[testDB].host.Memory = 1 << 30
	h.rollout(freeApp(), "dep-1")
	capacity, err := h.rt.GetClusterCapacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.AllocatableCPUMilli != 8000 || capacity.AllocatableMemBytes != 16<<30 || len(capacity.Nodes) != 1 {
		t.Fatalf("allocatable %+v", capacity)
	}
	if capacity.RequestedCPUMilli != 1000+50 || capacity.RequestedMemBytes != 1<<30+128<<20 {
		t.Fatalf("requested %dm %d", capacity.RequestedCPUMilli, capacity.RequestedMemBytes)
	}
	live, err := h.rt.LiveAppPods(context.Background(), testDB, "app-01")
	if err != nil || live.Count != 1 || live.CPUMilli != 50 || live.MemBytes != 128<<20 {
		t.Fatalf("live %+v (%v)", live, err)
	}
}

func TestPrivateNetworkIsAlwaysOpenAndCannotBeClosed(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	if open, err := h.rt.AppPrivateNetworkOpen(ctx, testDB); err != nil || !open {
		t.Fatalf("open = %v (%v)", open, err)
	}
	if err := h.rt.SetAppPrivateNetwork(ctx, testDB, true); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.SetAppPrivateNetwork(ctx, testDB, false); err == nil {
		t.Fatal("closed a network the host cannot close")
	}
}

func TestTeardownRemovesEveryTraceOfTheProject(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	if err := h.rt.CreateAppDisk(ctx, testDB, diskApp(), "", diskJobs); err != nil {
		t.Fatal(err)
	}
	h.rollout(diskApp(), "dep-1")
	if err := h.rt.TeardownProject(ctx, testProject, testDB); err != nil {
		t.Fatal(err)
	}
	if len(h.engine.appContainers("app-01")) != 0 || len(h.engine.volumes) != 0 || h.route() != "" {
		t.Fatal("project left behind")
	}
	if _, ok := h.engine.networks[testNet]; ok {
		t.Fatal("network left behind")
	}
	if err := h.rt.TeardownProject(ctx, testProject, testDB); err != nil {
		t.Fatalf("a second teardown: %v", err)
	}
}

// The cluster's bookkeeping has nothing to keep on a single host and must not fail the caller.
func TestClusterOnlyBookkeepingIsANoOp(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	placement, err := h.rt.RuntimeClassPlacement(ctx, "runsc")
	errs := []error{err,
		h.rt.AttachIssuedAppHostCertificates(ctx),
		h.rt.DeleteRegistryPullSecrets(ctx, testDB, "web"),
		h.rt.EnsureNamespaceQuota(ctx, testDB, k8s.NamespaceQuota{}),
		h.rt.RestartFunctionRuntime(ctx, testDB),
	}
	if joined := errors.Join(errs...); joined != nil || !reflect.DeepEqual(placement, k8s.RuntimePlacement{}) {
		t.Fatalf("%+v %v", placement, joined)
	}
}
