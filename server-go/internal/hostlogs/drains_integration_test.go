//go:build integration

package hostlogs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/testutil/testca"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// receiver is a real HTTPS log intake that keeps what it was sent.
type receiver struct {
	mu      sync.Mutex
	server  *httptest.Server
	bodies  []string
	headers []string
}

func newReceiver(t *testing.T, ca testca.CA, name string) *receiver {
	t.Helper()
	issued := ca.Issue(t, name)
	pair, err := tls.X509KeyPair([]byte(issued.CertPEM), []byte(issued.KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	rec := &receiver{}
	rec.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, string(body))
		rec.headers = append(rec.headers, r.Header.Get("Authorization"))
		rec.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	rec.server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	rec.server.StartTLS()
	t.Cleanup(rec.server.Close)
	return rec
}

func (r *receiver) received() (string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.bodies, "\n"), append([]string(nil), r.headers...)
}

// senderTo verifies TLS against ca and sends each public name to its local
// receiver, standing in for DNS; the SSRF dialer is proven by its own tests and the boot check.
func senderTo(ca testca.CA, routes map[string]*receiver) *logdrain.Sender {
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			target, ok := routes[host]
			if err != nil || !ok {
				return nil, fmt.Errorf("no receiver for %q", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, target.server.Listener.Addr().String())
		},
	}
	return logdrain.NewSender(&http.Client{Transport: transport, Timeout: 15 * time.Second})
}

func unsealedVault(t *testing.T) (*vault.Vault, *vault.MemoryStore) {
	t.Helper()
	store := vault.NewMemoryStore()
	v, err := vault.NewWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	init, err := v.Init(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Unseal(init.Shares[0]); err != nil {
		t.Fatal(err)
	}
	return v, store
}

// drainBench is a single host's drain service on a real Loki and vault, its
// internal forwarder routes, and a forwarder sending to two real receivers.
type drainBench struct {
	svc        *service.LogDrainService
	store      *fakestore.LogDrains
	rawVault   *vault.MemoryStore
	pusher     *LokiPusher
	forwarder  *logdrain.Forwarder
	recA, recB *receiver
	mu         sync.Mutex
	clock      time.Time
}

func (b *drainBench) now() time.Time { b.mu.Lock(); defer b.mu.Unlock(); return b.clock }

func (b *drainBench) setNow(at time.Time) { b.mu.Lock(); defer b.mu.Unlock(); b.clock = at }

func newDrainBench(t *testing.T, created time.Time) *drainBench {
	t.Helper()
	lokiURL := startBundledLoki(t)
	v, rawStore := unsealedVault(t)
	instances := map[string]*domain.DatabaseInstance{
		"drain-a": {ProjectID: "drain-a", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker},
		"drain-b": {ProjectID: "drain-b", DBType: domain.MySQL, DeploymentMode: domain.ModeDocker},
	}
	bench := &drainBench{store: fakestore.NewLogDrains(), rawVault: rawStore, clock: created,
		pusher: NewLokiPusher(lokiURL, NewPushClient())}
	bench.svc = service.NewLogDrainService(service.LogDrainDeps{Store: bench.store, Vault: v, SingleHost: true, Now: bench.now,
		Logs: service.NewLokiReader(lokiURL, &http.Client{}),
		Instances: func(id string) (*domain.DatabaseInstance, error) {
			if inst, ok := instances[id]; ok {
				return inst, nil
			}
			return nil, service.ErrProjectNotFound
		}})
	const token = "forwarder-token-integration"
	router := chi.NewRouter()
	handler.NewLogForwarderHandler(bench.svc, token).Mount(router, custommw.RequireInternalListener)
	internal := httptest.NewServer(custommw.MarkInternalListener(router))
	t.Cleanup(internal.Close)
	ca := testca.New(t, "drain receivers")
	bench.recA, bench.recB = newReceiver(t, ca, "logs-a.example.com"), newReceiver(t, ca, "logs-b.example.com")
	bench.forwarder = logdrain.NewForwarder(logdrain.NewHTTPControlPlane(internal.URL, token, internal.Client()),
		senderTo(ca, map[string]*receiver{"logs-a.example.com": bench.recA, "logs-b.example.com": bench.recB}))
	return bench
}

func (b *drainBench) addDrain(t *testing.T, projectID, url, secret string) *domain.LogDrain {
	t.Helper()
	drain, err := b.svc.Create(context.Background(), projectID, "admin", service.CreateLogDrainInput{Name: projectID,
		Kind: logdrain.KindHTTPS, URL: url, HeaderName: "Authorization", Secret: secret})
	if err != nil {
		t.Fatalf("drain of %s: %v", projectID, err)
	}
	return drain
}

// pushLines stores what the shipper would: each project's database line, a
// selector-shaped line, and a watcher line that is not the database's.
func (b *drainBench) pushLines(t *testing.T, at int64) {
	t.Helper()
	streams := []Stream{
		{Labels: Labels{ProjectID: "drain-a", Component: domain.ContainerDatabase, Container: "drain-a-postgres"},
			Entries: []Entry{{At: at, Line: "LOG: line of a"}, {At: at + 1, Line: `LOG: } or {project_id="drain-b"}`}}},
		{Labels: Labels{ProjectID: "drain-a", Component: domain.ContainerWatcher, Container: "drain-a-watcher"},
			Entries: []Entry{{At: at, Line: "watcher line of a"}}},
		{Labels: Labels{ProjectID: "drain-b", Component: domain.ContainerDatabase, Container: "drain-b-mysql"},
			Entries: []Entry{{At: at, Line: "LOG: line of b"}}},
	}
	if err := b.pusher.Push(context.Background(), streams); err != nil {
		t.Fatalf("push: %v", err)
	}
}

// forwardUntilBoth runs the forwarder until each receiver has its line.
func (b *drainBench) forwardUntilBoth(t *testing.T) (string, string) {
	t.Helper()
	var gotA, gotB string
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if _, err := b.forwarder.RunOnce(context.Background()); err != nil {
			t.Fatalf("forward: %v", err)
		}
		gotA, _ = b.recA.received()
		gotB, _ = b.recB.received()
		if strings.Contains(gotA, "line of a") && strings.Contains(gotB, "line of b") {
			break
		}
	}
	return gotA, gotB
}

// EXC-658 on a single host: lines the shipper stored in the bundled Loki go,
// through provisioning's internal routes and the forwarder, to their own
// project's drain only, never a sibling component's or another project's.
func TestSingleHostDrainsDeliverEachProjectsLinesToItsOwnDrainOnly(t *testing.T) {
	created := time.Now().Add(-2 * time.Minute)
	bench := newDrainBench(t, created)
	drainA := bench.addDrain(t, "drain-a", "https://logs-a.example.com/ingest?token=path-secret-a", "Bearer header-secret-a")
	bench.addDrain(t, "drain-b", "https://logs-b.example.com/ingest", "Bearer header-secret-b")
	bench.pushLines(t, created.Add(30*time.Second).UnixNano())
	bench.setNow(time.Now())

	gotA, gotB := bench.forwardUntilBoth(t)
	if !strings.Contains(gotA, "LOG: line of a") || !strings.Contains(gotB, "LOG: line of b") {
		t.Fatalf("not delivered: a=%q b=%q", gotA, gotB)
	}
	if strings.Contains(gotA, "line of b") || strings.Contains(gotB, "line of a") || strings.Contains(gotB, `project_id=\"drain-b\"`) {
		t.Errorf("a line crossed projects: a=%q b=%q", gotA, gotB)
	}
	if strings.Contains(gotA, "watcher line") {
		t.Errorf("a watcher line was forwarded: %q", gotA)
	}
	var entries []map[string]string
	if err := json.Unmarshal([]byte(strings.Split(gotA, "\n")[0]), &entries); err != nil || len(entries) == 0 || entries[0]["project"] != "drain-a" {
		t.Errorf("body %q: %v", gotA, err)
	}
	if _, headers := bench.recA.received(); len(headers) == 0 || headers[0] != "Bearer header-secret-a" {
		t.Errorf("drain a was sent headers %q", headers)
	}
	assertDeliveredAndEncrypted(t, bench, drainA)
}

func assertDeliveredAndEncrypted(t *testing.T, bench *drainBench, drainA *domain.LogDrain) {
	t.Helper()
	stored, err := bench.store.GetLogDrain(context.Background(), "drain-a", drainA.ID)
	if err != nil {
		t.Fatalf("drain a: %v", err)
	}
	if stored.LastError != "" || stored.CursorNS <= drainA.CursorNS || stored.Status != domain.LogDrainActive {
		t.Errorf("drain a after delivery: %+v", stored)
	}
	if strings.Contains(stored.URL, "path-secret-a") {
		t.Errorf("the shown URL carries its token: %s", stored.URL)
	}
	raw, err := bench.rawVault.GetSecret("projects/drain-a/logdrains/" + drainA.ID)
	if err != nil {
		t.Fatalf("raw vault entry: %v", err)
	}
	if len(raw) == 0 || strings.Contains(string(raw), "header-secret-a") || strings.Contains(string(raw), "path-secret-a") {
		t.Errorf("the drain's key or URL is not encrypted at rest (%d bytes)", len(raw))
	}
}
