package engineproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fencedPolicy() Policy {
	policy := testPolicy()
	policy.FenceProjectBridges = true
	return policy
}

func networkBody(t *testing.T, name string, internal bool, options map[string]string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"Name": name, "Driver": "bridge", "Internal": internal, "Options": options,
		"Labels": map[string]string{ManagedLabel: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAProjectBridgeNameFitsAnInterfaceAndTheFirewallsWildcard(t *testing.T) {
	name := ProjectBridgeName("excalibase-net-a-project-with-a-long-name")
	if !strings.HasPrefix(name, ProjectBridgePrefix) || len(name) > 15 || !interfaceName.MatchString(name) {
		t.Fatalf("bridge name %q", name)
	}
	if name == ProjectBridgeName("excalibase-net-another") {
		t.Fatal("two networks share a bridge name")
	}
}

// A network with a route or a host address must exist only under the firewall's wildcard.
func TestAFencingProxyAdmitsOnlyNetworksTheFirewallCovers(t *testing.T) {
	const name = "excalibase-proj-p1"
	named := map[string]string{bridgeNameOption: ProjectBridgeName(name)}
	isolated := map[string]string{gatewayModeIPv4: gatewayIsolated, gatewayModeIPv6: gatewayIsolated}
	cases := []struct {
		label    string
		network  string
		internal bool
		options  map[string]string
		admitted bool
	}{
		{"egress, its own name", name, false, named, true},
		{"egress, no name", name, false, nil, false},
		{"egress, another's name", name, false, map[string]string{bridgeNameOption: ProjectBridgeName("excalibase-proj-p2")}, false},
		{"egress, a name off the wildcard", name, false, map[string]string{bridgeNameOption: "br-open"}, false},
		{"internal with a host address, no name", name, true, nil, false},
		{"internal with a host address, its own name", name, true, named, true},
		{"internal and isolated, no name", name, true, isolated, true},
		{"internal, half isolated, no name", name, true, map[string]string{gatewayModeIPv4: gatewayIsolated}, false},
		{"the builds' sidecar network", "excalibase-proj-" + BuildEgressNetwork, false, nil, true},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			err := fencedPolicy().CheckNetworkCreate(networkBody(t, c.network, c.internal, c.options))
			if (err == nil) != c.admitted {
				t.Fatalf("admitted=%v, want %v: %v", err == nil, c.admitted, err)
			}
		})
	}
}

// Rootless Podman has no firewall to name a bridge for, and the name stays checked where it is sent.
func TestWithoutTheFenceAPlainNetworkIsAdmittedAndABridgeNameStillChecked(t *testing.T) {
	const name = "excalibase-proj-p1"
	if err := testPolicy().CheckNetworkCreate(networkBody(t, name, false, map[string]string{bridgeIsolate: "true"})); err != nil {
		t.Fatal(err)
	}
	if err := testPolicy().CheckNetworkCreate(networkBody(t, name, false, map[string]string{bridgeNameOption: "docker0"})); err == nil {
		t.Fatal("a create took another interface's name")
	}
}

const egressNet = "excalibase-proj-egress"

func egressApp(status string) *restartContainer {
	return &restartContainer{labels: map[string]string{ManagedLabel: "true", componentLabel: componentApp}, mode: egressNet, policy: "no", status: status}
}

func fencedEgress() map[string]map[string]any {
	return map[string]map[string]any{egressNet: {"Driver": "bridge", "Internal": false,
		"Options": map[string]string{bridgeNameOption: ProjectBridgeName(egressNet)}}}
}

func TestAnAppWithARouteOutIsCreatedWithoutAPolicyTheEngineWouldActOn(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{})
	engine.networks = fencedEgress()
	body := createBody(t, func(b map[string]any) {
		hostConfig(b)["NetworkMode"] = egressNet
		hostConfig(b)["PortBindings"] = nil
		hostConfig(b)["RestartPolicy"] = map[string]any{"Name": "unless-stopped", "MaximumRetryCount": 0}
		b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{egressNet: map[string]any{}}}
	})
	if resp := serve(t, handler, http.MethodPost, "/containers/create?name=excalibase-p1-app", string(body)); resp.StatusCode != http.StatusOK {
		t.Fatalf("create answered %d", resp.StatusCode)
	}
	if strings.Contains(engine.created, "unless-stopped") {
		t.Fatalf("the engine would bring it back at boot, ahead of the firewall: %s", engine.created)
	}
}

func TestAnAppWithARouteOutWaitsForTheFirewall(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"app": egressApp("created"), testFirewall: firewall("running", "starting")})
	engine.networks = fencedEgress()
	if resp := serve(t, handler, http.MethodPost, "/containers/app/start", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("start answered %d with the firewall starting", resp.StatusCode)
	}
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 0 {
		t.Fatalf("started %v with the firewall starting", started)
	}
	engine.containers[testFirewall].health = "healthy"
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 1 || started[0] != "app" {
		t.Fatalf("started %v once healthy, want the app the refused start left wanted", started)
	}
}

// A network from before this release has a br-<id> bridge no rule covers: nothing starts on it, firewall healthy or not.
func TestNothingStartsOnAProjectBridgeOffTheWildcard(t *testing.T) {
	crashed := egressApp("exited")
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"app": egressApp("created"), "crashed": crashed, testFirewall: firewall("running", "healthy")})
	engine.networks = map[string]map[string]any{egressNet: {"Driver": "bridge", "Internal": false, "Options": map[string]string{}}}
	if resp := serve(t, handler, http.MethodPost, "/containers/app/start", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("start answered %d on an unfenced bridge", resp.StatusCode)
	}
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 0 {
		t.Fatalf("started %v on an unfenced bridge", started)
	}
	engine.networks = fencedEgress() // provisioning's reconcile remade it
	handler.superviseOnce(context.Background())
	if len(engine.startedOnly()) != 2 {
		t.Fatalf("started %v once remade", engine.startedOnly())
	}
}

// An engine that cannot say what a network is fails closed.
func TestAnUnreadableProjectNetworkCountsAsNeedingTheFirewall(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"app": egressApp("created"), testFirewall: firewall("running", "starting")})
	engine.networks = map[string]map[string]any{}
	if resp := serve(t, handler, http.MethodPost, "/containers/app/start", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
}

func TestACrashedAppComesBackAndAStoppedOneStays(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"crashed": egressApp("exited"), "paused": egressApp("running"), testFirewall: firewall("running", "healthy")})
	engine.networks = fencedEgress()
	if resp := serve(t, handler, http.MethodPost, "/containers/paused/stop", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop answered %d", resp.StatusCode)
	}
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 1 || started[0] != "crashed" {
		t.Fatalf("started %v, want the crashed app alone", started)
	}
}

// Rollouts and Studio read an app's restarts from RestartCount, which only the engine's own policy raises.
func TestAnAppsRestartsReadAsTheEngineWouldCountThem(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"crashed": egressApp("exited"), "db": tenant("exited"), testFirewall: firewall("running", "healthy")})
	engine.networks = fencedEgress()
	readCount := func(id string) float64 {
		t.Helper()
		resp := serve(t, handler, http.MethodGet, "/containers/"+id+"/json", "")
		defer resp.Body.Close()
		var inspected map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&inspected); err != nil {
			t.Fatal(err)
		}
		if inspected["Id"] != id {
			t.Fatalf("inspect lost its fields: %v", inspected)
		}
		count, _ := inspected["RestartCount"].(float64)
		return count
	}
	if got := readCount("crashed"); got != 0 {
		t.Fatalf("count %v before any restart", got)
	}
	for want := 1.0; want <= 2; want++ {
		handler.superviseOnce(context.Background())
		if got := readCount("crashed"); got != want {
			t.Fatalf("count %v, want %v", got, want)
		}
		engine.containers["crashed"].status = "exited"
	}
	if got := readCount("db"); got != 0 {
		t.Fatalf("a tenant's count changed to %v", got)
	}
}

func TestAnAppFromAnEarlierReleaseIsTakenOffTheEnginesPolicy(t *testing.T) {
	older := egressApp("running")
	older.policy = "unless-stopped"
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"app": older, testFirewall: firewall("running", "starting")})
	engine.networks = fencedEgress()
	handler.superviseOnce(context.Background())
	if older.policy != "no" {
		t.Fatalf("policy %q", older.policy)
	}
}

// APP_EGRESS turned back to none: the network is isolated again and the engine's policy is safe, so it does the job.
func TestAnAppBackOnAnIsolatedNetworkReturnsToTheEnginesPolicy(t *testing.T) {
	crashed, paused := egressApp("exited"), egressApp("exited")
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"crashed": crashed, "paused": paused, testFirewall: firewall("running", "healthy")})
	engine.networks = fencedEgress()
	if resp := serve(t, handler, http.MethodPost, "/containers/paused/stop", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop answered %d", resp.StatusCode)
	}
	engine.networks = nil // the fake answers isolated
	handler.superviseOnce(context.Background())
	if crashed.policy != "unless-stopped" || paused.policy != "unless-stopped" {
		t.Fatalf("policies %q %q", crashed.policy, paused.policy)
	}
	if started := engine.startedOnly(); len(started) != 1 || started[0] != "crashed" {
		t.Fatalf("started %v, want the crashed app, not the paused one", started)
	}
}

// Docker 26 ignores the gateway option: what the engine reports decides, not what was asked.
func TestANetworkWithAGatewayIsNeverReadAsNothingToGuard(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"app": egressApp("created"), testFirewall: firewall("running", "starting")})
	engine.networks = map[string]map[string]any{egressNet: {"Driver": "bridge", "Internal": true,
		"Options": map[string]string{gatewayModeIPv4: gatewayIsolated, gatewayModeIPv6: gatewayIsolated},
		"IPAM":    map[string]any{"Config": []map[string]string{{"Subnet": "172.30.0.0/16", "Gateway": "172.30.0.1"}}}}}
	if resp := serve(t, handler, http.MethodPost, "/containers/app/start", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("start answered %d on a network with a host address", resp.StatusCode)
	}
}

// A database's mode is the tenants' network; it is the older bridge it is also attached to that its default route takes.
func TestTheSupervisorHoldsWhatIsAlsoAttachedToAnOlderBridge(t *testing.T) {
	db := tenant("exited")
	db.networks = []string{"excalibase-tenants", egressNet}
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"db": db, testFirewall: firewall("running", "healthy")})
	engine.networks = map[string]map[string]any{egressNet: {"Driver": "bridge", "Internal": false}}
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 0 {
		t.Fatalf("started %v attached to an unfenced bridge", started)
	}
	engine.networks = fencedEgress()
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 1 {
		t.Fatalf("started %v once remade", started)
	}
}

// The engine zeroes its count on a start through the API; a resumed app gets its fresh attempts.
func TestAStartThroughTheAPIClearsTheCountedRestarts(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"crashed": egressApp("exited"), testFirewall: firewall("running", "healthy")})
	engine.networks = fencedEgress()
	handler.superviseOnce(context.Background())
	if handler.restarts.restartsOf("crashed") != 1 {
		t.Fatalf("counted %d", handler.restarts.restartsOf("crashed"))
	}
	engine.containers["crashed"].status = "exited"
	if resp := serve(t, handler, http.MethodPost, "/containers/crashed/start", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
	if handler.restarts.restartsOf("crashed") != 0 {
		t.Fatalf("counted %d after a start", handler.restarts.restartsOf("crashed"))
	}
}

// A remade network takes an app back under its one service name, and no other.
func TestAnAppRejoinsItsOwnNetworkUnderItsOwnServiceName(t *testing.T) {
	app := egressApp("running")
	app.labels[appNameLabel] = "web"
	sibling := egressApp("running")
	sibling.mode = "excalibase-proj-other"
	sibling.labels[appNameLabel] = "web"
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"app": app, "elsewhere": sibling})
	engine.networks = fencedEgress()
	join := func(container, alias string) error {
		body, err := json.Marshal(map[string]any{"Container": container, "EndpointConfig": map[string]any{"Aliases": []string{alias}}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/networks/"+egressNet+"/connect", strings.NewReader(string(body)))
		return handler.authorize(req)
	}
	if err := join("app", "web."+AppServiceDomain); err != nil {
		t.Fatalf("its own name: %v", err)
	}
	for label, refused := range map[string]func() error{
		"a sibling's name":      func() error { return join("app", "db."+AppServiceDomain) },
		"another network's":     func() error { return join("elsewhere", "web."+AppServiceDomain) },
		"a name off the domain": func() error { return join("app", "web.example.com") },
	} {
		if err := refused(); err == nil {
			t.Errorf("%s was admitted", label)
		}
	}
}

func TestTheBuildsSidecarNetworkNeedsNoFirewall(t *testing.T) {
	sidecar := "excalibase-proj-" + BuildEgressNetwork
	step := &restartContainer{labels: map[string]string{ManagedLabel: "true", componentLabel: "build"}, mode: sidecar, policy: "no", status: "created"}
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"egress": step, testFirewall: firewall("running", "starting")})
	engine.networks = map[string]map[string]any{sidecar: {"Driver": "bridge", "Internal": false}}
	if resp := serve(t, handler, http.MethodPost, "/containers/egress/start", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
}

// A pause's stop landing inside the supervisor's pass, after it read the record and before its start.
func TestAPauseDuringASupervisorPassKeepsTheReplicaDown(t *testing.T) {
	engine := &restartEngine{containers: map[string]*restartContainer{"crashed": egressApp("exited"), testFirewall: firewall("running", "healthy")}, networks: fencedEgress()}
	var proxyURL atomic.Value
	var firewallReads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(stripVersion(r.URL.Path), "/containers/"+testFirewall+"/json") && firewallReads.Add(1) == 2 {
			resp, err := http.Post(proxyURL.Load().(string)+"/containers/crashed/stop", "application/json", nil)
			if err != nil {
				t.Error(err)
			} else {
				resp.Body.Close()
			}
		}
		engine.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	handler, err := NewHandler(testPolicy(), target, http.DefaultTransport).WithRestarts(Restarts{FirewallContainer: testFirewall, StoppedFile: filepath.Join(t.TempDir(), "stopped")})
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	proxyURL.Store(proxy.URL)
	handler.superviseOnce(context.Background())
	handler.superviseOnce(context.Background())
	if engine.containers["crashed"].status == "running" || !handler.restarts.isStopped("crashed") {
		t.Fatalf("a paused replica is %s, its record %v", engine.containers["crashed"].status, handler.restarts.isStopped("crashed"))
	}
}

// A stop that arrives while the start is on its way to the engine waits for it, then stops what it
// started; let through at once it would land first, and the start after it would win.
func TestAStopArrivingDuringASupervisorStartWinsOverIt(t *testing.T) {
	engine := &restartEngine{containers: map[string]*restartContainer{"crashed": egressApp("exited"), testFirewall: firewall("running", "healthy")}, networks: fencedEgress()}
	var proxyURL atomic.Value
	answered := make(chan int, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/crashed/start") {
			go func() {
				resp, err := http.Post(proxyURL.Load().(string)+"/containers/crashed/stop", "application/json", nil)
				if err != nil {
					t.Error(err)
					answered <- 0
					return
				}
				resp.Body.Close()
				answered <- resp.StatusCode
			}()
			time.Sleep(100 * time.Millisecond)
		}
		engine.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	handler, err := NewHandler(testPolicy(), target, http.DefaultTransport).WithRestarts(Restarts{FirewallContainer: testFirewall, StoppedFile: filepath.Join(t.TempDir(), "stopped")})
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	proxyURL.Store(proxy.URL)
	handler.superviseOnce(context.Background())
	if status := <-answered; status != http.StatusNoContent {
		t.Fatalf("stop answered %d", status)
	}
	handler.superviseOnce(context.Background())
	if engine.containers["crashed"].status != "exited" || !handler.restarts.isStopped("crashed") {
		t.Fatalf("status %s, record %v", engine.containers["crashed"].status, handler.restarts.isStopped("crashed"))
	}
}

// A start the proxy gave up on may still land in the engine after a stop: that is not "started by hand".
func TestAStartThatTimedOutCannotOutliveAStop(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"crashed": egressApp("exited"), testFirewall: firewall("running", "healthy")})
	engine.networks = fencedEgress()
	engine.hang = map[string]bool{"crashed": true}
	handler.restarts.startTimeout = 50 * time.Millisecond
	handler.superviseOnce(context.Background())
	engine.mu.Lock()
	engine.hang = nil
	engine.mu.Unlock()
	if err := handler.restarts.setStopped("crashed", true); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	engine.containers["crashed"].status = "running" // the hung start landed after all
	engine.mu.Unlock()

	handler.superviseOnce(context.Background())
	if engine.containers["crashed"].status != "exited" || !handler.restarts.isStopped("crashed") {
		t.Fatalf("status %s, record %v", engine.containers["crashed"].status, handler.restarts.isStopped("crashed"))
	}
}

// Under Docker's policy a crashed replica reads as restarting, and everything that asks "is it paused?"
// (a disk move above all) relies on that: exited means stopped on purpose.
func TestACrashedAppTheProxyWillRestartReadsAsRestarting(t *testing.T) {
	paused := egressApp("running")
	db := tenant("exited")
	isolatedApp := egressApp("exited")
	isolatedApp.mode = "excalibase-proj-isolated"
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"crashed": egressApp("exited"), "paused": paused, "db": db, "isolated": isolatedApp, testFirewall: firewall("running", "starting")})
	engine.networks = fencedEgress()
	engine.networks["excalibase-proj-isolated"] = map[string]any{"Driver": "bridge", "Internal": true,
		"Options": map[string]string{gatewayModeIPv4: gatewayIsolated, gatewayModeIPv6: gatewayIsolated}}
	if resp := serve(t, handler, http.MethodPost, "/containers/paused/stop", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop answered %d", resp.StatusCode)
	}
	inspectState := func(id string) (string, bool) {
		t.Helper()
		resp := serve(t, handler, http.MethodGet, "/containers/"+id+"/json", "")
		defer resp.Body.Close()
		var inspected struct {
			State struct {
				Status     string
				Restarting bool
			}
		}
		if err := json.NewDecoder(resp.Body).Decode(&inspected); err != nil {
			t.Fatal(err)
		}
		return inspected.State.Status, inspected.State.Restarting
	}
	if status, restarting := inspectState("crashed"); status != "restarting" || !restarting {
		t.Fatalf("a crashed app inspects as %s restarting=%v", status, restarting)
	}
	for _, id := range []string{"paused", "db", "isolated"} {
		if status, restarting := inspectState(id); status != "exited" || restarting {
			t.Fatalf("%s inspects as %s restarting=%v", id, status, restarting)
		}
	}
	resp := serve(t, handler, http.MethodGet, "/containers/json?all=1&filters="+url.QueryEscape(`{"label":{"excalibase.managed=true":true}}`), "")
	defer resp.Body.Close()
	var list []struct {
		ID    string `json:"Id"`
		State string
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"crashed": "restarting", "paused": "exited", "db": "exited", "isolated": "exited"}
	for _, listed := range list {
		if listed.State != want[listed.ID] {
			t.Errorf("%s lists as %s, want %s", listed.ID, listed.State, want[listed.ID])
		}
	}
	if len(list) != len(want) {
		t.Fatalf("listed %v", list)
	}
}

// Nothing managed runs off the firewall's wildcard: Docker's default bridge has no rule of ours.
func TestAFencingProxyAdmitsNoContainerOnTheDefaultBridgeOrTheBuildsNetwork(t *testing.T) {
	p := fencedPolicy()
	body := func(mode, endpoints string) []byte {
		return []byte(`{"Image":"x","Labels":{"excalibase.managed":"true"},"HostConfig":{"NetworkMode":"` + mode + `"},"NetworkingConfig":{"EndpointsConfig":{` + endpoints + `}}}`)
	}
	for label, create := range map[string][]byte{
		"docker0 by name":         body("bridge", ""),
		"docker0 by default":      body("default", ""),
		"docker0 by no mode":      body("", ""),
		"the builds' network":     body(p.NetworkPrefix+BuildEgressNetwork, ""),
		"the builds' as endpoint": body("", `"`+p.NetworkPrefix+BuildEgressNetwork+`":{}`),
	} {
		if err := p.CheckCreate("excalibase-appx", create); err == nil {
			t.Errorf("%s was admitted", label)
		}
	}
	for label, create := range map[string][]byte{
		"a tenant on its network": body("", `"excalibase-tenants":{}`),
		"no network":              body("none", ""),
		"a project network":       body("excalibase-proj-p1", ""),
	} {
		if err := p.CheckCreate("excalibase-appx", create); err != nil {
			t.Errorf("%s: %v", label, err)
		}
	}
	if err := testPolicy().CheckCreate("excalibase-appx", body("bridge", "")); err != nil {
		t.Errorf("rootless Podman, with no fence: %v", err)
	}
}

func TestOnlyTheBuildsProxyJoinsTheirNetwork(t *testing.T) {
	sidecar := &restartContainer{labels: map[string]string{ManagedLabel: "true", componentLabel: "build"}, mode: "excalibase-proj-build-1", policy: "no", status: "created", entrypoint: []string{BuildEgressEntrypoint}}
	app := egressApp("running")
	app.entrypoint = []string{"/app/run"}
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"sidecar": sidecar, "app": app})
	engine.networks = fencedEgress()
	handler.policy.FenceProjectBridges = true
	join := func(container string) error {
		body, err := json.Marshal(map[string]any{"Container": container})
		if err != nil {
			t.Fatal(err)
		}
		return handler.authorize(httptest.NewRequest(http.MethodPost, "/networks/excalibase-proj-"+BuildEgressNetwork+"/connect", strings.NewReader(string(body))))
	}
	if err := join("sidecar"); err != nil {
		t.Fatalf("the sidecar: %v", err)
	}
	if err := join("app"); err == nil {
		t.Fatal("an app joined the builds' network")
	}
}

func TestListingProjectNetworksIsFilteredToManagedOnes(t *testing.T) {
	_, handler, _ := restartProxy(t, map[string]*restartContainer{})
	if err := handler.authorize(httptest.NewRequest(http.MethodGet, "/networks?filters="+url.QueryEscape(`{"label":{"excalibase.managed=true":true}}`), nil)); err != nil {
		t.Fatal(err)
	}
	if err := handler.authorize(httptest.NewRequest(http.MethodGet, "/networks", nil)); err == nil {
		t.Fatal("an unfiltered list was admitted")
	}
}

// Once the platform has started it again the given-up start is history: a later start by hand is one by hand.
func TestAStartByTheAPIEndsTheDoubtOverAGivenUpStart(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"crashed": egressApp("exited"), testFirewall: firewall("running", "healthy")})
	engine.networks = fencedEgress()
	engine.hang = map[string]bool{"crashed": true}
	handler.restarts.startTimeout = 50 * time.Millisecond
	handler.superviseOnce(context.Background())
	engine.mu.Lock()
	engine.hang = nil
	engine.mu.Unlock()
	if resp := serve(t, handler, http.MethodPost, "/containers/crashed/start", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
	if resp := serve(t, handler, http.MethodPost, "/containers/crashed/stop", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop answered %d", resp.StatusCode)
	}
	engine.mu.Lock()
	engine.containers["crashed"].status = "running" // docker start, on the host
	engine.mu.Unlock()
	handler.superviseOnce(context.Background())
	if engine.containers["crashed"].status != "running" || handler.restarts.isStopped("crashed") {
		t.Fatalf("status %s, record %v", engine.containers["crashed"].status, handler.restarts.isStopped("crashed"))
	}
}
