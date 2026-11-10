package provisioner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/engineproxy"
)

const firewallName = "excalibase-host-firewall"

// behindGuard is the real client through the real proxy, on an engine with one tenant and the firewall.
func behindGuard(t *testing.T, health string, firewallRunning bool) *RealDockerClient {
	t.Helper()
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+firewallName+"/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": firewallName,
				"State": map[string]any{"Status": "running", "Running": firewallRunning, "Health": map[string]any{"Status": health}}})
		case strings.HasSuffix(r.URL.Path, "/excalibase-p1-postgres/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "excalibase-p1-postgres", "State": map[string]any{"Status": "exited"},
				"Config":     map[string]any{"Labels": map[string]string{engineproxy.ManagedLabel: "true"}},
				"HostConfig": map[string]any{"NetworkMode": "excalibase-tenants"}})
		case strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "old", "Names": []string{"/excalibase-p1-postgres-pre-upgrade-mu-1"}}})
		default:
			w.Header().Set("Api-Version", "1.47")
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(engine.Close)
	target, err := url.Parse(engine.URL)
	if err != nil {
		t.Fatal(err)
	}
	policy := engineproxy.Policy{ManagedLabel: engineproxy.ManagedLabel, Networks: []string{"excalibase-tenants"},
		ContainerPrefix: "excalibase-", StatsContainers: []string{firewallName}}
	handler, err := engineproxy.NewHandler(policy, target, http.DefaultTransport).WithRestarts(engineproxy.Restarts{
		FirewallContainer: firewallName, StoppedFile: filepath.Join(t.TempDir(), "stopped")})
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)
	client, err := NewRealDockerClient(DockerClientOptions{Host: "tcp://" + strings.TrimPrefix(proxy.URL, "http://"), FirewallContainer: firewallName})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// A teardown looks for what upgrades kept through the proxy, which lists only what is filtered to managed.
func TestTheProxyAnswersTheListATeardownAsksFor(t *testing.T) {
	names, err := behindGuard(t, "healthy", true).ManagedContainerNames(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(names) != 1 || names[0] != "excalibase-p1-postgres-pre-upgrade-mu-1" {
		t.Fatalf("names %v", names)
	}
}

// The proxy's refusal reaches the caller as the one error recovery acts on, and no other failure does.
func TestTheProxysRefusalIsHostFirewallDown(t *testing.T) {
	client := behindGuard(t, "starting", true)
	if err := client.StartContainer(context.Background(), "excalibase-p1-postgres"); !errors.Is(err, ErrHostFirewallDown) {
		t.Fatalf("start = %v", err)
	}
	if err := client.StartContainer(context.Background(), "excalibase-provisioning"); err == nil || errors.Is(err, ErrHostFirewallDown) {
		t.Fatalf("a start refused for another reason: %v", err)
	}
	if err := RequireHostFirewall(context.Background(), client); !errors.Is(err, ErrHostFirewallDown) {
		t.Fatalf("require = %v", err)
	}
}

func TestRequireHostFirewallReadsItThroughTheProxy(t *testing.T) {
	if err := RequireHostFirewall(context.Background(), behindGuard(t, "healthy", true)); err != nil {
		t.Fatalf("healthy: %v", err)
	}
	if err := RequireHostFirewall(context.Background(), behindGuard(t, "healthy", false)); !errors.Is(err, ErrHostFirewallDown) {
		t.Fatalf("exited with a stale health: %v", err)
	}
	client := behindGuard(t, "unhealthy", true)
	client.firewall = "excalibase-no-such-container"
	if err := RequireHostFirewall(context.Background(), client); !errors.Is(err, ErrHostFirewallDown) {
		t.Fatalf("not readable: %v", err)
	}
	client.firewall = ""
	if err := RequireHostFirewall(context.Background(), client); err != nil {
		t.Fatalf("no firewall on this engine: %v", err)
	}
}

type refusingStarts struct {
	DockerClient
	refusals int
	err      error
}

func (r *refusingStarts) StartContainer(context.Context, string) error {
	if r.refusals > 0 {
		r.refusals--
		return r.err
	}
	return nil
}

func TestStartHoldingPortWaitsOutTheFirewall(t *testing.T) {
	poller := NewPoller(time.Millisecond, time.Second)
	refused := &refusingStarts{refusals: 3, err: ErrHostFirewallDown}
	if err := StartHoldingPort(context.Background(), refused, poller, "c"); err != nil || refused.refusals != 0 {
		t.Fatalf("err %v, refusals left %d", err, refused.refusals)
	}
	always := &refusingStarts{refusals: 1 << 30, err: ErrHostFirewallDown}
	if err := StartHoldingPort(context.Background(), always, NewPoller(time.Millisecond, 10*time.Millisecond), "c"); !errors.Is(err, ErrHostFirewallDown) {
		t.Fatalf("outlasting the wait = %v", err)
	}
}
