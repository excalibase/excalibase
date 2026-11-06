package dockerapps

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/engineproxy"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-687: a project bridge with a route or a host address carries a name under the
// firewall's wildcard from the moment the engine makes it.

func projectBridge(h *harness) string {
	return h.engine.networks[testNet].options.Options[bridgeNameOption]
}

func TestAnEgressBridgeIsNamedUnderTheFirewallsWildcard(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress = true })
	verified(t, h)
	h.rollout(freeApp(), "dep-1")
	if got, want := projectBridge(h), engineproxy.ProjectBridgeName(testNet); got != want {
		t.Fatalf("bridge %q, want %q", got, want)
	}
}

func TestAnEngineWithoutTheIsolatedGatewayNamesItsBridges(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.gatewayMode = "refuse"
	verified(t, h)
	h.rollout(freeApp(), "dep-1")
	if got, want := projectBridge(h), engineproxy.ProjectBridgeName(testNet); got != want {
		t.Fatalf("bridge %q, want %q", got, want)
	}
	for name, net := range h.engine.networks {
		if strings.Contains(name, "check-") {
			t.Fatalf("the check network %s outlived the check: %+v", name, net.options)
		}
	}
}

// The default install changes nothing: an isolated internal network has nothing to guard, and no remake.
func TestAnIsolatedInternalBridgeKeepsTheEnginesName(t *testing.T) {
	h := newHarness(t, nil)
	verified(t, h)
	h.rollout(freeApp(), "dep-1")
	if got := projectBridge(h); got != "" {
		t.Fatalf("bridge named %q on an isolated network", got)
	}
	h.engine.calls = nil
	if err := h.rt.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(h.engine.calls, func(call string) bool { return strings.HasPrefix(call, "network ") }) {
		t.Fatalf("an isolated network was remade: %v", h.engine.calls)
	}
}

func TestPodmanIsNeverSentABridgeName(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Isolate = true })
	h.engine.podman = true
	verified(t, h)
	h.rollout(freeApp(), "dep-1")
	if got := projectBridge(h); got != "" {
		t.Fatalf("Podman was sent bridge %q", got)
	}
}

// An upgrade: an egress network of an earlier release has a br-<id> bridge no rule covers.
func TestAnOlderEgressBridgeIsRemadeUnderTheWildcard(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress = true })
	verified(t, h)
	h.rollout(freeApp(), "dep-1")
	apps := h.appContainers()
	delete(h.engine.networks[testNet].options.Options, bridgeNameOption)
	if err := h.rt.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := projectBridge(h), engineproxy.ProjectBridgeName(testNet); got != want {
		t.Fatalf("bridge %q after the reconcile, want %q", got, want)
	}
	if members := h.engine.networks[testNet].members; !slices.Contains(members, apps[0]) || !slices.Contains(members, testDB) {
		t.Fatalf("members %v after the remake", members)
	}
	if aliases := h.engine.containers[apps[0]].aliases[testNet]; len(aliases) != 1 || aliases[0] != "web."+engineproxy.AppServiceDomain {
		t.Fatalf("the app came back as %v: its project reaches it by that name", aliases)
	}
}

func TestEgressBehindTheFirewallCountsAsGuarded(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress, o.HostFirewall = true, true })
	verified(t, h)
	if isolated, why := h.rt.HostIsolation(); !isolated {
		t.Fatalf("not counted as guarded: %s", why)
	}
}

func TestDockerBefore28BehindTheFirewallCountsAsGuarded(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.HostFirewall = true })
	h.engine.gatewayMode = "refuse"
	verified(t, h)
	if isolated, why := h.rt.HostIsolation(); !isolated {
		t.Fatalf("not counted as guarded: %s", why)
	}
}

// The warning says what is really exposed, not only the host's services.
func TestEgressWithTheFirewallOffSaysWhatItExposes(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress = true })
	verified(t, h)
	isolated, why := h.rt.HostIsolation()
	for _, named := range []string{"metadata", "private", "exnet+"} {
		if isolated || !strings.Contains(why, named) {
			t.Fatalf("isolated=%v why=%q, want %q named", isolated, why, named)
		}
	}
}

// No firewall sees what pasta sends as this user's own connections, so it takes an explicit yes.
func TestRootlessPodmanRefusesEgressUntilAccepted(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress, o.Isolate = true, true })
	h.engine.podman = true
	err := h.rt.VerifyEngine(context.Background())
	if err == nil {
		t.Fatal("rootless Podman started with egress nothing can guard")
	}
	for _, named := range []string{"APP_EGRESS_UNGUARDED=accept", "metadata", "LAN"} {
		if !strings.Contains(err.Error(), named) {
			t.Fatalf("%q does not name %q", err, named)
		}
	}
	h = newHarness(t, func(o *Options) { o.Egress, o.Isolate, o.AcceptUnguardedEgress = true, true, true })
	h.engine.podman = true
	verified(t, h)
	if isolated, why := h.rt.HostIsolation(); isolated || !strings.Contains(why, "metadata") {
		t.Fatalf("accepted: isolated=%v why=%q", isolated, why)
	}
}

// One project's bad record must not leave the others on bridges no rule covers.
func TestOneBadRecordDoesNotStopTheOtherProjectsRemake(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress = true })
	verified(t, h)
	h.rollout(freeApp(), "dep-1")
	delete(h.engine.networks[testNet].options.Options, bridgeNameOption)
	broken := &appRecord{Project: "proj-broken", AppID: "app-x", AppName: "x", Port: 8080, Host: "x.apps.example.com", Serving: []string{"not an upstream!"}}
	raw, err := json.Marshal(broken)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomically(h.rt.recordPath(broken.Project, broken.AppID, ".json"), raw); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.Reconcile(context.Background()); err == nil {
		t.Fatal("the bad record went unreported")
	}
	if got, want := projectBridge(h), engineproxy.ProjectBridgeName(testNet); got != want {
		t.Fatalf("bridge %q after a reconcile with a bad record elsewhere, want %q", got, want)
	}
}

func TestWithdrawingAProjectStopsAReplicaThatCrashedToo(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress = true })
	h.rollout(freeApp(), "dep-1")
	crashed := h.appContainers()[0]
	h.engine.containers[crashed].state = "exited"
	name := h.engine.containers[crashed].name
	h.engine.calls = nil
	if err := h.rt.WithdrawProjectWorkloads(context.Background(), testDB, k8s.WithdrawOptions{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.engine.calls, "stop "+name) {
		t.Fatalf("calls %v, want a stop of the crashed replica", h.engine.calls)
	}
}

// With the proxy looking after crashes, a crashed replica must not come back during a pause.
func TestAPauseStopsAReplicaThatCrashedToo(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress = true })
	h.rollout(freeApp(), "dep-1")
	crashed := h.appContainers()[0]
	h.engine.containers[crashed].state = "exited"
	name := h.engine.containers[crashed].name
	h.engine.calls = nil
	if err := h.rt.PauseAppWorkload(context.Background(), testDB, "app-01"); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(h.engine.calls, func(call string) bool { return call == "stop "+name }) {
		t.Fatalf("calls %v, want a stop of the crashed replica, which records it", h.engine.calls)
	}
}
