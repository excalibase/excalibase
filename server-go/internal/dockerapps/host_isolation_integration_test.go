//go:build integration

package dockerapps

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/network"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-676 on the real engine, through the real proxy: an app cannot connect to a
// host service listening on every address through its network's bridge address.
//
// Run: go test -tags=integration ./internal/dockerapps/ -run TestRealEngineKeepsAppsOffTheHost -v
func TestRealEngineKeepsAppsOffTheHost(t *testing.T) {
	rig := newRealRig(t)
	if isolated, why := rig.rt.HostIsolation(); !isolated {
		t.Skipf("this engine cannot isolate project networks from the host: %s", why)
	}
	port := hostListener(t)
	// An earlier release made project networks with an address on the host.
	rig.rt.setHostIsolation(hostIsolation{})
	rig.deploy(itApp("one"), "dep-1")
	app := rig.serving()
	gateway := rig.projectGateway()
	if gateway == "" {
		t.Fatal("an older project network has no gateway to reproduce through")
	}
	if !rig.reachesHost(app, gateway, port) {
		t.Log("the host already drops bridge traffic; the closed checks below prove less")
	}
	before := rig.started(app)

	rig.rt.setHostIsolation(hostIsolation{gateway: true, isolated: true})
	if err := rig.rt.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := rig.projectGateway(); got != "" {
		t.Fatalf("the remade network still has the host address %s", got)
	}
	if rig.reachesHost(app, gateway, port) {
		t.Fatalf("the app still reaches the host at %s:%d", gateway, port)
	}
	if after := rig.started(app); after != before {
		t.Fatalf("the remake restarted the app (started %s, then %s)", before, after)
	}
	if body, err := rig.fromEdge(app); err != nil || body != "one" {
		t.Fatalf("the edge lost the app after the remake: %q (%v)", body, err)
	}
	// The engine echoes the options back, so the next pass leaves the network alone.
	remade := rig.projectNetworkID()
	if err := rig.rt.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again := rig.projectNetworkID(); again != remade {
		t.Fatalf("a remade network was remade again (%s, then %s)", remade, again)
	}

	// A paused app, off the network while stopped, comes back on the remade one.
	ctx := context.Background()
	if err := rig.rt.PauseAppWorkload(ctx, rig.db, "app-it"); err != nil {
		t.Fatal(err)
	}
	if err := rig.rt.ResumeAppWorkload(ctx, rig.db, "app-it", "web", domain.Free, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	resumed := rig.serving()
	if body, err := rig.fromEdge(resumed); err != nil || body != "one" {
		t.Fatalf("after resume on the remade network: %q (%v)", body, err)
	}
	if rig.reachesHost(resumed, gateway, port) {
		t.Fatal("the resumed app reaches the host")
	}
}

// hostListener answers on every address of the host, as sshd or a local database does.
func hostListener(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("host"))
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().(*net.TCPAddr).Port
}

func (r *realRig) projectGateway() string {
	r.t.Helper()
	inspect, err := r.direct.NetworkInspect(context.Background(), r.rt.networkName("proj-it"), network.InspectOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	for _, config := range inspect.IPAM.Config {
		if config.Gateway != "" {
			return config.Gateway
		}
	}
	return ""
}

func (r *realRig) projectNetworkID() string {
	r.t.Helper()
	inspect, err := r.direct.NetworkInspect(context.Background(), r.rt.networkName("proj-it"), network.InspectOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	return inspect.ID
}

func (r *realRig) reachesHost(app, address string, port int) bool {
	target := fmt.Sprintf("http://%s/", net.JoinHostPort(address, fmt.Sprint(port)))
	output, err := exec.Command("docker", "exec", app, "wget", "-qO-", "-T", "3", target).CombinedOutput()
	return err == nil && strings.TrimSpace(string(output)) == "host"
}

func (r *realRig) started(app string) string {
	r.t.Helper()
	inspect, err := r.direct.ContainerInspect(context.Background(), app)
	if err != nil {
		r.t.Fatal(err)
	}
	return inspect.State.StartedAt
}
