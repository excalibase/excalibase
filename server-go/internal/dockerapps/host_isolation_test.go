package dockerapps

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// EXC-676: an internal project network still gives its bridge an address on the
// host, and every host service listening on all addresses answers there.

func verified(t *testing.T, h *harness) {
	t.Helper()
	if err := h.rt.VerifyEngine(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func projectNetworkOption(h *harness) (string, bool) {
	option, set := h.engine.networks[testNet].options.Options[gatewayModeIPv4]
	return option, set
}

func TestProjectNetworksHaveNoHostAddressOnDocker28(t *testing.T) {
	h := newHarness(t, nil)
	verified(t, h)
	h.rollout(freeApp(), "dep-1")
	net := h.engine.networks[testNet].options
	if !net.Internal || net.Options[gatewayModeIPv4] != gatewayIsolated || net.Options[gatewayModeIPv6] != gatewayIsolated {
		t.Fatalf("project network %+v, want internal with an isolated gateway", net)
	}
	if isolated, why := h.rt.HostIsolation(); !isolated {
		t.Fatalf("not counted as isolated: %s", why)
	}
}

// The startup check makes its network the way project networks are made, so the
// name resolution it proves is the one apps get.
func TestTheStartupCheckResolvesNamesOnAnIsolatedNetwork(t *testing.T) {
	h := newHarness(t, nil)
	var checked []string
	h.engine.toolOutput = func(cmd []string) (string, int) {
		if cmd[0] == "nslookup" {
			for name, net := range h.engine.networks {
				if strings.HasPrefix(name, "excalibase-net-check-") && net.options.Options[gatewayModeIPv4] == gatewayIsolated {
					checked = append(checked, name)
				}
			}
		}
		return "", 0
	}
	verified(t, h)
	if len(checked) != 1 {
		t.Fatalf("names resolved on isolated check networks %v, want one", checked)
	}
}

func TestAnEngineWithoutTheIsolatedGatewayKeepsPlainInternalNetworksAndSaysSo(t *testing.T) {
	for mode, want := range map[string]string{"refuse": "Docker 28", "ignore": "Docker 28"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, nil)
			h.engine.gatewayMode = mode
			verified(t, h)
			isolated, why := h.rt.HostIsolation()
			if isolated || !strings.Contains(why, want) || !strings.Contains(why, "firewall") {
				t.Fatalf("isolated=%v why=%q, want the upgrade and the firewall named", isolated, why)
			}
			h.rollout(freeApp(), "dep-1")
			if option, set := projectNetworkOption(h); set {
				t.Fatalf("an engine that does not honour it was sent gateway mode %q", option)
			}
		})
	}
}

// A proxy or engine refusing the check for another reason is not read as an old engine.
func TestTheStartupCheckFailsOnAnyOtherRefusal(t *testing.T) {
	h := newHarness(t, nil)
	for _, mode := range []string{"forbid", "invalid"} {
		h.engine.gatewayMode = mode
		if err := h.rt.VerifyEngine(context.Background()); err == nil {
			t.Fatalf("%s: a refused check network passed as an engine without the isolated gateway", mode)
		}
	}
}

func TestPodmanIsNeverSentDockersGatewayMode(t *testing.T) {
	for _, rootful := range []bool{false, true} {
		h := newHarness(t, func(o *Options) { o.Isolate = true })
		h.engine.podman, h.engine.rootful = true, rootful
		verified(t, h)
		h.rollout(freeApp(), "dep-1")
		if _, set := projectNetworkOption(h); set {
			t.Fatal("Podman was sent Docker's gateway mode, which netavark refuses")
		}
		isolated, why := h.rt.HostIsolation()
		// Rootless: the bridge's address is inside the rootless network namespace, not the host's.
		if isolated == rootful || (rootful && !strings.Contains(why, "firewall")) {
			t.Fatalf("rootful=%v: isolated=%v why=%q", rootful, isolated, why)
		}
	}
}

func TestEgressNetworksKeepTheirGatewayAndSaySo(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress = true })
	verified(t, h)
	h.rollout(freeApp(), "dep-1")
	if _, set := projectNetworkOption(h); set || h.engine.networks[testNet].options.Internal {
		t.Fatalf("an egress network %+v", h.engine.networks[testNet].options)
	}
	if isolated, why := h.rt.HostIsolation(); isolated || !strings.Contains(why, "APP_EGRESS") {
		t.Fatalf("isolated=%v why=%q", isolated, why)
	}
}

// An upgrade: networks made by an earlier release are remade without a host address,
// their apps kept running (not restarted) and every member put back.
func TestReconcileRemakesAnOlderProjectNetworkKeepingItsApps(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.EdgeAliases = []string{"api.example.com"} })
	ctx := context.Background()
	h.rollout(freeApp(), "dep-1")
	if _, set := projectNetworkOption(h); set {
		t.Fatal("before the check, the network was made as an earlier release made it")
	}
	running := h.appContainers()
	stopped := "stopped-app"
	h.engine.addContainer(stopped, "excalibase-app-stopped", map[string]string{
		labelManaged: "true", labelComponent: componentApp, labelProject: testProject, labelApp: "app-02",
	})
	h.engine.containers[stopped].state = "exited"
	if err := h.engine.NetworkConnect(ctx, testNet, stopped, nil); err != nil {
		t.Fatal(err)
	}
	verified(t, h)
	h.engine.calls = nil
	if err := h.rt.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if option, _ := projectNetworkOption(h); option != gatewayIsolated {
		t.Fatalf("the older network was kept: %+v", h.engine.networks[testNet].options)
	}
	members := h.engine.networks[testNet].members
	for _, member := range append([]string{testDB, "edge01", stopped}, running...) {
		if !slices.Contains(members, member) {
			t.Errorf("%s is not back on the network (members %v)", member, members)
		}
	}
	if aliases := h.engine.containers["edge01"].aliases[testNet]; !slices.Equal(aliases, []string{"api.example.com"}) {
		t.Error("the edge came back without the platform's hostnames")
	}
	for _, call := range h.engine.calls {
		if strings.HasPrefix(call, "start ") || strings.HasPrefix(call, "stop ") || strings.HasPrefix(call, "remove ") {
			t.Errorf("the remake touched a container: %s", call)
		}
	}
	h.engine.calls = nil
	if err := h.rt.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(h.engine.calls, func(call string) bool { return strings.HasPrefix(call, "network ") }) {
		t.Fatalf("an isolated network was remade again: %v", h.engine.calls)
	}
}

// A remake that stopped after the network was removed is finished by the next pass: the apps come back.
func TestAnInterruptedRemakeIsFinishedByTheNextReconcile(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.rollout(freeApp(), "dep-1")
	running := h.appContainers()
	for _, member := range append([]string{testDB, "edge01"}, running...) {
		if err := h.engine.NetworkDisconnect(ctx, testNet, member, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.engine.NetworkRemove(ctx, testNet); err != nil {
		t.Fatal(err)
	}
	verified(t, h)
	if err := h.rt.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	for _, member := range running {
		if !slices.Contains(h.engine.networks[testNet].members, member) {
			t.Fatalf("app %s was left off its remade network", member)
		}
	}
}

func TestOlderNetworksAreKeptWhereTheEngineCannotIsolate(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.gatewayMode = "refuse"
	h.rollout(freeApp(), "dep-1")
	verified(t, h)
	h.engine.calls = nil
	if err := h.rt.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(h.engine.calls, func(call string) bool { return strings.HasPrefix(call, "network ") }) {
		t.Fatalf("a network was remade on an engine that cannot isolate: %v", h.engine.calls)
	}
}

func (h *harness) appContainers() []string {
	h.t.Helper()
	list, err := h.engine.ContainerList(context.Background(), container.ListOptions{All: true,
		Filters: labelFilter(labelComponent, componentApp, labelProject, testProject)})
	if err != nil || len(list) == 0 {
		h.t.Fatalf("app containers %v (%v)", list, err)
	}
	ids := make([]string, 0, len(list))
	for _, summary := range list {
		ids = append(ids, summary.ID)
	}
	return ids
}

// An app left off an already-remade network (a pass stopped after the create) is put back.
func TestReconcilePutsBackAnAppOffAnIsolatedNetwork(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	verified(t, h)
	h.rollout(freeApp(), "dep-1")
	app := h.appContainers()[0]
	if err := h.engine.NetworkDisconnect(ctx, testNet, app, true); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.engine.networks[testNet].members, app) {
		t.Fatal("the app was left off its project's network")
	}
}

// A stopped database is not listed among the members, yet it must come off before the remove.
func TestTheRemakeTakesAStoppedDatabaseOffToo(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.rollout(freeApp(), "dep-1")
	h.engine.containers[testDB].state = "exited"
	verified(t, h)
	if err := h.rt.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if option, _ := projectNetworkOption(h); option != gatewayIsolated || !slices.Contains(h.engine.networks[testNet].members, testDB) {
		t.Fatalf("network %+v members %v", h.engine.networks[testNet].options, h.engine.networks[testNet].members)
	}
}

// A remake whose remove fails leaves the project as it was, not offline.
func TestAFailedRemakePutsTheMembersBack(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.EdgeAliases = []string{"api.example.com"} })
	ctx := context.Background()
	h.rollout(freeApp(), "dep-1")
	before := slices.Clone(h.engine.networks[testNet].members)
	verified(t, h)
	h.engine.removeErr = errors.New("network has active endpoints")
	if err := h.rt.Reconcile(ctx); err == nil {
		t.Fatal("a failed remake was not reported")
	}
	after := h.engine.networks[testNet].members
	for _, member := range before {
		if !slices.Contains(after, member) {
			t.Errorf("%s is off the network after a failed remake (members %v)", member, after)
		}
	}
	if aliases := h.engine.containers["edge01"].aliases[testNet]; !slices.Equal(aliases, []string{"api.example.com"}) {
		t.Errorf("the edge came back with aliases %v", aliases)
	}
}

func TestEgressNetworksAreNotRemade(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress = true })
	h.rollout(freeApp(), "dep-1")
	verified(t, h)
	h.engine.calls = nil
	if err := h.rt.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(h.engine.calls, func(call string) bool { return strings.HasPrefix(call, "network ") }) {
		t.Fatalf("an egress network was remade: %v", h.engine.calls)
	}
}

// Egress turned off after networks were made with it: they lose their route and host address.
func TestNetworksMadeWithEgressAreRemadeWhenItIsTurnedOff(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Egress = true })
	h.rollout(freeApp(), "dep-1")
	h.rt.opts.Egress = false
	verified(t, h)
	if err := h.rt.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if net := h.engine.networks[testNet].options; !net.Internal || net.Options[gatewayModeIPv4] != gatewayIsolated {
		t.Fatalf("the egress network was kept: %+v", net)
	}
}
