package dockerapps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	testDB      = "db0001"
	testProject = "proj-abc"
	testNet     = "excalibase-net-proj-abc"
)

type fakeScopes map[string]string // namespace -> project

func (s fakeScopes) ProjectOf(namespace string) (string, error) {
	if project, ok := s[namespace]; ok {
		return project, nil
	}
	return "", errors.New("no project has namespace " + namespace)
}

func (s fakeScopes) NamespaceOf(projectID string) (string, error) {
	for namespace, project := range s {
		if project == projectID {
			return namespace, nil
		}
	}
	return "", errors.New("no namespace for " + projectID)
}

type harness struct {
	t      *testing.T
	rt     *Runtime
	engine *fakeEngine
	dir    string
}

func testOptions(dir string) Options {
	return Options{
		NetworkPrefix: "excalibase-net-", VolumePrefix: "excalibase-vol-",
		EdgeContainer: "excalibase-edge", EdgeTLSAddress: "excalibase-edge:443", RoutesDir: dir,
		SandboxRuntime: "runsc", ProbeBinary: []byte("probe"), ToolsImage: "tools:1",
		Route: apphost.Route{Domain: "apps.example.com", TLS: true}, ReservedHosts: []string{"studio.example.com"},
		Projects: fakeScopes{testDB: testProject}, PollInterval: time.Millisecond, MinReady: 0, CrashRestarts: 3,
	}
}

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	engine := newFakeEngine()
	engine.addContainer(testDB, "excalibase-proj-abc-postgres", map[string]string{labelManaged: "true"})
	engine.addContainer("edge01", "excalibase-edge", nil)
	opts := testOptions(t.TempDir())
	if mutate != nil {
		mutate(&opts)
	}
	rt, err := New(engine, opts)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, rt: rt, engine: engine, dir: opts.RoutesDir}
}

func (h *harness) deploy(app *apphost.App, deployID string) {
	h.t.Helper()
	opts := renderOptions()
	opts.DeployID = deployID
	workload, err := k8s.RenderAppWorkload(testDB, app, stubResolver{}, opts)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.rt.ApplyAppWorkload(context.Background(), testDB, workload); err != nil {
		h.t.Fatalf("apply %s: %v", deployID, err)
	}
}

func (h *harness) rollout(app *apphost.App, deployID string) {
	h.t.Helper()
	h.deploy(app, deployID)
	if err := h.rt.WaitForAppRollout(context.Background(), testDB, k8s.AppObjectName(app.Name), deployID, time.Second); err != nil {
		h.t.Fatalf("rollout %s: %v", deployID, err)
	}
}

func (h *harness) route() string {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.dir, testProject+".app-01.caddy"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return string(raw)
}

func freeApp() *apphost.App {
	app := testApp()
	app.Tier, app.Replicas = domain.Free, 1
	return app
}

func TestApplyRunsTheAppOnItsProjectNetworkUnderTheSandbox(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")

	net := h.engine.networks[testNet]
	if net == nil || !net.options.Internal || net.options.Labels[labelManaged] != "true" || len(net.options.Options) != 0 {
		t.Fatalf("project network %+v", net)
	}
	if !slices.Contains(net.members, testDB) || !slices.Contains(net.members, "edge01") {
		t.Fatalf("network members %v, want the project's database and the edge", net.members)
	}
	apps := h.engine.appContainers("app-01")
	if len(apps) != 1 || apps[0].state != "running" {
		t.Fatalf("app containers %+v", apps)
	}
	c := apps[0]
	host := c.host
	if host.Runtime != "runsc" || !slices.Equal(host.CapDrop, []string{"NET_RAW"}) || len(host.CapAdd) != 0 {
		t.Fatalf("sandboxed app: runtime %q drop %v add %v", host.Runtime, host.CapDrop, host.CapAdd)
	}
	if !slices.Contains(host.SecurityOpt, "no-new-privileges") || host.Privileged || len(host.Binds) != 0 || len(host.PortBindings) != 0 {
		t.Fatalf("hardening %+v", host)
	}
	if host.Memory != 256<<20 || host.MemorySwap != host.Memory || host.NanoCPUs != 250_000_000 || host.PidsLimit == nil || *host.PidsLimit != appPidsLimit {
		t.Fatalf("limits memory %d swap %d cpu %d pids %v", host.Memory, host.MemorySwap, host.NanoCPUs, host.PidsLimit)
	}
	if string(host.NetworkMode) != testNet || !slices.Equal(c.networks, []string{testNet}) {
		t.Fatalf("network mode %q networks %v", host.NetworkMode, c.networks)
	}
	if string(c.files[probePath]) != "probe" {
		t.Fatalf("probe not copied in: %v", c.files)
	}
	if c.config.Image != "docker.io/library/busybox:1.37" || !slices.Contains(h.engine.pulled, c.config.Image) {
		t.Fatalf("image %q pulled %v", c.config.Image, h.engine.pulled)
	}
	if route := h.route(); !strings.Contains(route, "web-abc.apps.example.com {") || !strings.Contains(route, "respond") {
		t.Fatalf("before the rollout the host answers 503:\n%s", route)
	}
}

func TestApplyWithoutASandboxDropsEveryCapability(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.SandboxRuntime = "" })
	h.deploy(freeApp(), "dep-1")
	host := h.engine.appContainers("app-01")[0].host
	if host.Runtime != "" || !slices.Equal(host.CapDrop, []string{"ALL"}) || !slices.Equal(host.CapAdd, []string{"NET_BIND_SERVICE"}) {
		t.Fatalf("unsandboxed app: runtime %q drop %v add %v", host.Runtime, host.CapDrop, host.CapAdd)
	}
}

func TestApplyFailsWhenThePullFails(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.pullErr["docker.io/library/busybox:1.37"] = "pull access denied"
	opts := renderOptions()
	workload, err := k8s.RenderAppWorkload(testDB, freeApp(), stubResolver{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	err = h.rt.ApplyAppWorkload(context.Background(), testDB, workload)
	if err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("err = %v, want the pull failure", err)
	}
	if len(h.engine.appContainers("app-01")) != 0 {
		t.Fatal("a container was created without its image")
	}
}

func TestRolloutRoutesToTheNewContainersAndRemovesTheOldOnes(t *testing.T) {
	h := newHarness(t, nil)
	h.rollout(freeApp(), "dep-1")
	first := h.engine.appContainers("app-01")[0].name
	if route := h.route(); !strings.Contains(route, "reverse_proxy "+first+":8080") {
		t.Fatalf("route after the first rollout:\n%s", route)
	}
	h.rollout(freeApp(), "dep-2")
	apps := h.engine.appContainers("app-01")
	if len(apps) != 1 || apps[0].name == first || apps[0].config.Labels[labelDeploy] != "dep-2" {
		t.Fatalf("after the second rollout: %+v", apps)
	}
	if route := h.route(); !strings.Contains(route, apps[0].name+":8080") || strings.Contains(route, first) {
		t.Fatalf("route after the second rollout:\n%s", route)
	}
}

func TestRolloutThatNeverBecomesReadyFailsAndKeepsTheOldVersionServing(t *testing.T) {
	h := newHarness(t, nil)
	h.rollout(freeApp(), "dep-1")
	serving := h.engine.appContainers("app-01")[0].name
	h.engine.probeOK = func(c *fakeContainer) bool { return c.config.Labels[labelDeploy] != "dep-2" }
	h.deploy(freeApp(), "dep-2")
	err := h.rt.WaitForAppRollout(context.Background(), testDB, "app-web", "dep-2", 30*time.Millisecond)
	if !errors.Is(err, k8s.ErrAppRollout) {
		t.Fatalf("err = %v, want ErrAppRollout", err)
	}
	if route := h.route(); !strings.Contains(route, serving) {
		t.Fatalf("the old version stopped serving:\n%s", route)
	}
	if available, err := h.rt.AppAvailableReplicas(context.Background(), testDB, "app-web"); err != nil || available != 1 {
		t.Fatalf("available = %d (%v), want the old version's one", available, err)
	}
}

func TestRolloutOfACrashingContainerFailsFast(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")
	c := h.engine.appContainers("app-01")[0]
	c.state, c.restarts = "restarting", 3
	err := h.rt.WaitForAppRollout(context.Background(), testDB, "app-web", "dep-1", time.Minute)
	if !errors.Is(err, k8s.ErrAppRollout) || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("err = %v, want a crash-loop failure", err)
	}
}

func TestInternalServiceHasNoRouteAndTheEdgeStaysOff(t *testing.T) {
	h := newHarness(t, nil)
	app := freeApp()
	app.Internal, app.Port, app.HealthCheckPath = true, 0, ""
	app.InternalPorts = []apphost.InternalPort{{Port: 6379, Protocol: "TCP"}}
	h.rollout(app, "dep-1")
	if route := h.route(); route != "" {
		t.Fatalf("internal service routed:\n%s", route)
	}
	if slices.Contains(h.engine.networks[testNet].members, "edge01") {
		t.Fatal("the edge joined a project with no public app")
	}
}

func TestEgressNetworksAreIsolatedOnPodmanOnly(t *testing.T) {
	docker := newHarness(t, func(o *Options) { o.Egress = true })
	docker.deploy(freeApp(), "dep-1")
	if net := docker.engine.networks[testNet]; net.options.Internal || len(net.options.Options) != 0 {
		t.Fatalf("docker egress network %+v", net.options)
	}
	podman := newHarness(t, func(o *Options) { o.Egress, o.Isolate = true, true })
	podman.deploy(freeApp(), "dep-1")
	if net := podman.engine.networks[testNet]; net.options.Internal || net.options.Options["isolate"] != "true" {
		t.Fatalf("podman egress network %+v", net.options)
	}
}

func TestNewRefusesAnIncompleteSetup(t *testing.T) {
	for name, mutate := range map[string]func(*Options){
		"no network prefix": func(o *Options) { o.NetworkPrefix = "" },
		"no volume prefix":  func(o *Options) { o.VolumePrefix = "" },
		"no edge":           func(o *Options) { o.EdgeContainer = "" },
		"no edge address":   func(o *Options) { o.EdgeTLSAddress = "" },
		"no routes dir":     func(o *Options) { o.RoutesDir = "" },
		"no probe":          func(o *Options) { o.ProbeBinary = nil },
		"no tools image":    func(o *Options) { o.ToolsImage = "" },
		"no app domain":     func(o *Options) { o.Route.Domain = "" },
		"no project lookup": func(o *Options) { o.Projects = nil },
		"platform prefix":   func(o *Options) { o.NetworkPrefix = "excalibase-" },
		"unwritable routes": func(o *Options) { o.RoutesDir = "/proc/excalibase-routes" },
	} {
		opts := testOptions(t.TempDir())
		mutate(&opts)
		if _, err := New(newFakeEngine(), opts); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVerifyEngineRefusesAMissingSandbox(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.runtimes = []string{"runc"}
	if err := h.rt.VerifyEngine(context.Background()); err == nil || !strings.Contains(err.Error(), "runsc") {
		t.Fatalf("err = %v, want the missing runtime named", err)
	}
	h.engine.runtimes = []string{"runc", "runsc"}
	if err := h.rt.VerifyEngine(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The tools image runs once under the sandbox at startup: a missing image or a
// runtime that cannot start a container refuses to start, not the first disk.
func TestVerifyEngineRunsTheToolsImageInTheSandbox(t *testing.T) {
	h := newHarness(t, nil)
	var ran [][]string
	h.engine.toolOutput = func(cmd []string) (string, int) { ran = append(ran, cmd); return "", 0 }
	if err := h.rt.VerifyEngine(context.Background()); err != nil || len(ran) != 2 || ran[0][0] != "true" {
		t.Fatalf("verify: %v, tool runs %v", err, ran)
	}
	h.engine.toolOutput = func([]string) (string, int) { return "", 1 }
	if err := h.rt.VerifyEngine(context.Background()); err == nil {
		t.Fatal("a tools image that cannot run passed")
	}
	h.rt.opts.ToolsImage = "missing:1"
	h.engine.toolOutput = nil
	if err := h.rt.VerifyEngine(context.Background()); err == nil {
		t.Fatal("a missing tools image passed")
	}
}

// Apps reach their database by its container name. gVisor's own network stack
// cannot reach the engine's resolver, so a sandbox that cannot resolve names is refused.
func TestVerifyEngineRefusesASandboxThatCannotResolveContainerNames(t *testing.T) {
	h := newHarness(t, nil)
	var lookups []string
	h.engine.toolOutput = func(cmd []string) (string, int) {
		if cmd[0] != "nslookup" {
			return "", 0
		}
		lookups = append(lookups, strings.Join(cmd, " "))
		for _, c := range h.engine.containers {
			if c.config.Cmd != nil && slices.Equal(append(slices.Clone(c.config.Entrypoint), c.config.Cmd...), cmd) &&
				(c.host.Runtime != "runsc" || !strings.HasPrefix(string(c.host.NetworkMode), "excalibase-net-check-")) {
				return "", 9
			}
		}
		return "", 2
	}
	err := h.rt.VerifyEngine(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--network=host") || len(lookups) != 1 {
		t.Fatalf("err = %v after %v, want the remedy named", err, lookups)
	}
	for name := range h.engine.networks {
		if strings.HasPrefix(name, "excalibase-net-check-") {
			t.Fatalf("the check left network %s", name)
		}
	}
	h.engine.toolOutput = func(cmd []string) (string, int) { return "", 0 }
	if err := h.rt.VerifyEngine(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIsPodmanReadsTheEnginesComponents(t *testing.T) {
	engine := newFakeEngine()
	if podman, err := IsPodman(context.Background(), engine); err != nil || podman {
		t.Fatalf("Docker: %v (%v)", podman, err)
	}
	engine.podman = true
	if podman, err := IsPodman(context.Background(), engine); err != nil || !podman {
		t.Fatalf("Podman: %v (%v)", podman, err)
	}
}
