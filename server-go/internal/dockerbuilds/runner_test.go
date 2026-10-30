package dockerbuilds

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const (
	testBuild    = "6f1c2a1e-0000-4000-8000-000000000001"
	testNode     = "node:22-bookworm@sha256:abc"
	testPrepare  = "busybox:1.37.0@sha256:def"
	testEgress   = "docker.io/excalibase/provisioning:ci"
	testToken    = "ghs_clone_token_value"
	testSecret   = "s3cret-value"
	buildNetwork = "excalibase-net-build-" + testBuild
	workVolume   = "excalibase-vol-build-" + testBuild
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newTestRunner(t *testing.T, engine *fakeEngine, sandbox string) (*Runner, *clock) {
	t.Helper()
	return newTestRunnerOn(t, engine, sandbox, false)
}

func newTestRunnerOn(t *testing.T, engine *fakeEngine, sandbox string, podman bool) (*Runner, *clock) {
	t.Helper()
	now := &clock{now: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	runner, err := NewRunner(engine, Options{
		NetworkPrefix: "excalibase-net-", VolumePrefix: "excalibase-vol-", SandboxRuntime: sandbox,
		EgressImage: testEgress, PrepareImage: testPrepare, ExtraDenyCIDRs: []string{"203.0.113.0/24"},
		EgressAllow: []string{"git.lan:8080"}, Now: now.Now, Isolate: podman,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner, now
}

func testSpec() sitebuilds.RunSpec {
	return sitebuilds.RunSpec{
		BuildID: testBuild, SiteID: "site-1", ProjectID: "proj-1", Image: testNode,
		CloneURL: "https://github.com/acme/site.git", Commit: "abc123", CloneToken: testToken,
		RootDir: "web", OutputDir: "dist", CPUMillis: 1500, Memory: "2Gi", Disk: "5Gi", Timeout: 10 * time.Minute,
		Env: map[string]string{"VITE_GREETING": "hi", "API_KEY": testSecret, "HOME": "/elsewhere"},
	}
}

// startBuild verifies the engine first, as provisioning does at startup, then starts the test build.
func startBuild(t *testing.T, runner *Runner) {
	t.Helper()
	if err := runner.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := runner.Start(context.Background(), testSpec()); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func env(c *fakeContainer) map[string]string {
	out := map[string]string{}
	for _, pair := range c.config.Env {
		name, value, _ := strings.Cut(pair, "=")
		out[name] = value
	}
	return out
}

func step(name string) string { return "excalibase-build-" + testBuild + "-" + name }

func TestNewRunnerRefusesMissingSettings(t *testing.T) {
	good := Options{NetworkPrefix: "excalibase-net-", VolumePrefix: "excalibase-vol-", EgressImage: testEgress, PrepareImage: testPrepare}
	for name, change := range map[string]func(*Options){
		"network prefix": func(o *Options) { o.NetworkPrefix = "" },
		"volume prefix":  func(o *Options) { o.VolumePrefix = "vol-" },
		"egress image":   func(o *Options) { o.EgressImage = "" },
		"prepare image":  func(o *Options) { o.PrepareImage = "" },
		"egress allow":   func(o *Options) { o.EgressAllow = []string{"no-port"} },
		"extra deny":     func(o *Options) { o.ExtraDenyCIDRs = []string{"nope"} },
	} {
		options := good
		change(&options)
		if _, err := NewRunner(newFakeEngine(), options); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if _, err := NewRunner(nil, good); err == nil {
		t.Fatal("no engine accepted")
	}
}

func TestVerifyNeedsTheSandboxRuntime(t *testing.T) {
	engine := newFakeEngine()
	engine.runtimes = []string{"runc"}
	runner, _ := newTestRunner(t, engine, "runsc")
	if err := runner.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "runsc") {
		t.Fatalf("Verify: %v", err)
	}
	optedOut, _ := newTestRunner(t, engine, "")
	if err := optedOut.Verify(context.Background()); err != nil {
		t.Fatalf("Verify without a sandbox: %v", err)
	}
}

func TestStartLaysOutTheBuild(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	assertNetworksAndVolume(t, engine)
	assertEgressSidecar(t, engine)
	for _, name := range []string{"prepare", "clone", "build", "collect"} {
		if engine.get(step(name)) == nil {
			t.Fatalf("step %s not created", name)
		}
	}
	if engine.get(step("prepare")).state != "running" || engine.get(step("clone")).state != "created" {
		t.Fatal("only prepare starts at once")
	}
}

func assertNetworksAndVolume(t *testing.T, engine *fakeEngine) {
	t.Helper()
	if options := engine.networks[buildNetwork]; !options.Internal || options.Labels["excalibase.managed"] != "true" ||
		options.Options["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" {
		t.Fatalf("build network %+v must be internal, managed and without an address on the host", options)
	}
	if options, ok := engine.networks["excalibase-net-build-egress"]; !ok || options.Internal {
		t.Fatalf("egress network %+v must exist with a route out", options)
	}
	if !engine.volumes[workVolume] {
		t.Fatal("no work volume")
	}
	if !slices.Contains(engine.pulled, testNode) || !slices.Contains(engine.pulled, testPrepare) || slices.Contains(engine.pulled, testEgress) {
		t.Fatalf("pulled %v: the build images, never provisioning's own", engine.pulled)
	}
}

func assertEgressSidecar(t *testing.T, engine *fakeEngine) {
	t.Helper()
	egress := engine.get(step("egress"))
	if egress == nil || egress.state != "running" {
		t.Fatalf("egress sidecar %+v must be running", egress)
	}
	if !slices.Contains(egress.networks, buildNetwork) || !slices.Contains(egress.networks, "excalibase-net-build-egress") {
		t.Fatalf("egress networks %v", egress.networks)
	}
	if e := env(egress); e["EGRESS_ALLOW"] != "git.lan:8080" || e["EGRESS_EXTRA_DENY_CIDRS"] != "203.0.113.0/24" {
		t.Fatalf("egress env %v", e)
	}
}

func TestStepsAreLockedDown(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	for _, name := range []string{"prepare", "clone", "build", "collect", "egress", "watch"} {
		assertLockedDown(t, name, engine.get(step(name)))
	}
	build := engine.get(step("build")).host
	if build.Memory != 2<<30 || build.MemorySwap != build.Memory || build.NanoCPUs != 1_500_000_000 || build.CPUShares != 256 {
		t.Fatalf("build limits: memory %d swap %d cpus %d shares %d", build.Memory, build.MemorySwap, build.NanoCPUs, build.CPUShares)
	}
	assertStepNetworks(t, engine)
	mounts := build.Mounts
	if len(mounts) != 1 || mounts[0].Source != workVolume || mounts[0].Target != "/work" {
		t.Fatalf("build mounts %+v", mounts)
	}
	if _, ok := build.Tmpfs["/tmp"]; !ok {
		t.Fatal("no /tmp")
	}
}

// assertLockedDown: no capability, no new privileges, read-only root, bounded processes, the expected runtime and user.
func assertLockedDown(t *testing.T, name string, planned *fakeContainer) {
	t.Helper()
	host := planned.host
	if !host.ReadonlyRootfs || !slices.Contains(host.CapDrop, "ALL") || len(host.CapAdd) > 0 || host.Privileged {
		t.Fatalf("%s: root %v caps %v/%v", name, host.ReadonlyRootfs, host.CapDrop, host.CapAdd)
	}
	if !slices.Contains(host.SecurityOpt, "no-new-privileges") || host.PidsLimit == nil || *host.PidsLimit <= 0 {
		t.Fatalf("%s: security %v pids %v", name, host.SecurityOpt, host.PidsLimit)
	}
	if len(host.Binds) > 0 || len(host.PortBindings) > 0 || planned.config.Labels["excalibase.managed"] != "true" {
		t.Fatalf("%s: binds %v ports %v labels %v", name, host.Binds, host.PortBindings, planned.config.Labels)
	}
	wantRuntime, wantUser := "runsc", "1000:1000"
	if name == "egress" || name == "watch" {
		wantRuntime = ""
	}
	if name == "prepare" {
		wantUser = "0:0"
	}
	if host.Runtime != wantRuntime || planned.config.User != wantUser {
		t.Fatalf("%s runs under %q as %q, want %q as %q", name, host.Runtime, planned.config.User, wantRuntime, wantUser)
	}
}

func assertStepNetworks(t *testing.T, engine *fakeEngine) {
	t.Helper()
	if string(engine.get(step("collect")).host.NetworkMode) != "none" || string(engine.get(step("prepare")).host.NetworkMode) != "none" {
		t.Fatal("prepare and collect need no network")
	}
	for _, name := range []string{"clone", "build"} {
		if string(engine.get(step(name)).host.NetworkMode) != buildNetwork {
			t.Fatalf("%s is on %q", name, engine.get(step(name)).host.NetworkMode)
		}
	}
}

func TestSandboxOptOutLeavesTheRuntimeDefault(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "")
	startBuild(t, runner)
	if runtime := engine.get(step("build")).host.Runtime; runtime != "" {
		t.Fatalf("runtime %q after the opt-out", runtime)
	}
}

func TestCredentialsReachOnlyTheirStep(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	for _, name := range []string{"prepare", "clone", "build", "collect", "egress", "watch"} {
		assertCredentialsOnlyWhereNeeded(t, name, engine.get(step(name)))
	}
	build := env(engine.get(step("build")))
	if build["HOME"] != "/work/home" || build["VITE_GREETING"] != "hi" || build["ROOT_DIR"] != "web" || build["NODE_USE_ENV_PROXY"] != "1" {
		t.Fatalf("build env %v", build)
	}
	clone := env(engine.get(step("clone")))
	if clone["CLONE_URL"] != "https://github.com/acme/site.git" || clone["COMMIT"] != "abc123" || clone["GIT_TOKEN"] != testToken {
		t.Fatalf("clone env %v", clone)
	}
	collect := env(engine.get(step("collect")))
	if collect["OUTPUT_DIR"] != "dist" || collect["ROOT_DIR"] != "web" {
		t.Fatalf("collect env %v", collect)
	}
	cmd := engine.get(step("clone")).config.Cmd
	if len(cmd) != 7 || cmd[4] != "bash" || cmd[5] != "-c" || !strings.Contains(cmd[6], "git fetch") {
		t.Fatalf("clone runs %v", cmd)
	}
}

// assertCredentialsOnlyWhereNeeded: the clone token in clone, the build's secrets in build, the proxy in both.
func assertCredentialsOnlyWhereNeeded(t *testing.T, name string, planned *fakeContainer) {
	t.Helper()
	joined := strings.Join(planned.config.Env, " ")
	if hasToken := strings.Contains(joined, testToken); hasToken != (name == "clone") {
		t.Fatalf("%s holds the clone token: %v", name, hasToken)
	}
	if hasSecret := strings.Contains(joined, testSecret); hasSecret != (name == "build") {
		t.Fatalf("%s holds the build secret: %v", name, hasSecret)
	}
	if name != "clone" && name != "build" {
		return
	}
	const proxy = "http://10.250.0.2:3128"
	values := env(planned)
	for _, variable := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "npm_config_https_proxy", "YARN_HTTPS_PROXY"} {
		if values[variable] != proxy {
			t.Fatalf("%s %s=%q, want the sidecar %s", name, variable, values[variable], proxy)
		}
	}
}

func TestStatusWalksTheSteps(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	ctx := context.Background()

	status := mustStatus(t, runner)
	if status.Phase != sitebuilds.RunRunning || status.CloneDone {
		t.Fatalf("while preparing: %+v", status)
	}
	engine.finish(step("prepare"), 0, false)
	if status = mustStatus(t, runner); status.Phase != sitebuilds.RunRunning || engine.get(step("clone")).state != "running" {
		t.Fatalf("clone did not start: %+v", status)
	}
	engine.finish(step("clone"), 0, false)
	status = mustStatus(t, runner)
	if !status.CloneDone || engine.get(step("build")).state != "running" {
		t.Fatalf("build did not start after the clone: %+v", status)
	}
	engine.finish(step("build"), 0, false)
	if status = mustStatus(t, runner); status.Phase != sitebuilds.RunRunning || engine.get(step("collect")).state != "running" {
		t.Fatalf("collect did not start: %+v", status)
	}
	if status = mustStatus(t, runner); status.Phase != sitebuilds.RunRunning {
		t.Fatalf("ready before the output was packed: %+v", status)
	}
	engine.get(step("collect")).files["/work/collect/ready"] = []byte{}
	if status, _ = runner.Status(ctx, testBuild); status.Phase != sitebuilds.RunReady {
		t.Fatalf("packed output not ready: %+v", status)
	}
}

func mustStatus(t *testing.T, runner *Runner) sitebuilds.RunStatus {
	t.Helper()
	status, err := runner.Status(context.Background(), testBuild)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return status
}

func TestStatusNamesFailures(t *testing.T) {
	cases := []struct {
		name string
		step string
		code int
		oom  bool
		want sitebuilds.FailureKind
	}{
		{"prepare", "prepare", 1, false, sitebuilds.FailPlatform},
		{"clone", "clone", 1, false, sitebuilds.FailClone},
		{"install", "build", 10, false, sitebuilds.FailInstall},
		{"build", "build", 11, false, sitebuilds.FailBuild},
		{"root dir", "build", 12, false, sitebuilds.FailRootDir},
		{"oom flag", "build", 0, true, sitebuilds.FailOutOfMemory},
		{"killed", "build", 137, false, sitebuilds.FailOutOfMemory},
		{"no output", "collect", 3, false, sitebuilds.FailNoOutput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			engine := newFakeEngine()
			runner, _ := newTestRunner(t, engine, "runsc")
			startBuild(t, runner)
			for _, earlier := range []string{"prepare", "clone", "build", "collect"} {
				if earlier == c.step {
					break
				}
				engine.finish(step(earlier), 0, false)
			}
			engine.finish(step(c.step), c.code, c.oom)
			status := mustStatus(t, runner)
			if status.Phase != sitebuilds.RunFailed || status.Failure != c.want {
				t.Fatalf("%+v, want %s", status, c.want)
			}
		})
	}
}

func TestStatusStopsABuildPastItsDeadline(t *testing.T) {
	engine := newFakeEngine()
	runner, now := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	now.now = now.now.Add(11 * time.Minute)
	status := mustStatus(t, runner)
	if status.Phase != sitebuilds.RunFailed || status.Failure != sitebuilds.FailTimeout {
		t.Fatalf("%+v", status)
	}
	if engine.get(step("prepare")).state == "running" {
		t.Fatal("the running step was not stopped")
	}
}

// The disk is measured from a container of its own, beside the steps: their user, no network, the volume read-only.
func TestStartWatchesTheDiskFromOutsideTheSteps(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	watch := engine.get(step("watch"))
	if watch == nil || watch.state != "running" {
		t.Fatalf("disk watcher %+v must be running from the start", watch)
	}
	if watch.config.User != "1000:1000" || string(watch.host.NetworkMode) != "none" || watch.config.Image != testPrepare || len(watch.config.Env) != 0 {
		t.Fatalf("watcher user %q network %q image %q env %v", watch.config.User, watch.host.NetworkMode, watch.config.Image, watch.config.Env)
	}
	if mounts := watch.host.Mounts; len(mounts) != 1 || mounts[0].Source != workVolume || !mounts[0].ReadOnly {
		t.Fatalf("watcher mounts %+v", mounts)
	}
	script := strings.Join(watch.config.Cmd, " ")
	for _, want := range []string{"du -sxk /work", "5242880", "df -Pk /work", "exit 3", "exit 4", "exit 5"} {
		if !strings.Contains(script, want) {
			t.Fatalf("watcher script lacks %q: %s", want, script)
		}
	}
}

func TestStatusStopsABuildPastItsDisk(t *testing.T) {
	cases := map[int]string{3: "5Gi", 4: "short of disk", 5: "could not be measured"}
	for code, want := range cases {
		engine := newFakeEngine()
		runner, _ := newTestRunner(t, engine, "runsc")
		startBuild(t, runner)
		engine.finish(step("prepare"), 0, false)
		mustStatus(t, runner) // starts clone
		engine.finish(step("watch"), code, false)
		status := mustStatus(t, runner)
		if status.Phase != sitebuilds.RunFailed || status.Failure != sitebuilds.FailDisk || !strings.Contains(status.Detail, want) {
			t.Fatalf("watcher exit %d: %+v", code, status)
		}
		if engine.get(step("clone")).state == "running" {
			t.Fatal("the clone was not stopped")
		}
	}
}

// Every container's log is capped, so output cannot fill the host's disk.
func TestStepLogsAreCapped(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	for _, name := range []string{"egress", "watch", "prepare", "clone", "build", "collect"} {
		logs := engine.get(step(name)).host.LogConfig
		if logs.Type != "json-file" || logs.Config["max-size"] == "" || logs.Config["max-file"] == "" {
			t.Fatalf("%s logs %+v", name, logs)
		}
	}
}

// The time limit holds in the engine too: each step is killed at the deadline even with provisioning gone.
func TestStepsEndAtTheDeadlineWithoutProvisioning(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	for _, name := range []string{"clone", "build", "collect"} {
		cmd := engine.get(step(name)).config.Cmd
		if len(cmd) < 4 || cmd[0] != "timeout" || cmd[1] != "-s" || cmd[2] != "KILL" || cmd[3] != "600" {
			t.Fatalf("%s cmd %v", name, cmd)
		}
	}
	if e := env(engine.get(step("egress"))); e["EGRESS_LIFETIME"] != "660s" {
		t.Fatalf("egress lifetime %q", e["EGRESS_LIFETIME"])
	}
}

func TestStatusOfAVanishedBuildIsAPlatformFailure(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	status, err := runner.Status(context.Background(), testBuild)
	if err != nil || status.Phase != sitebuilds.RunFailed || status.Failure != sitebuilds.FailPlatform {
		t.Fatalf("%+v %v", status, err)
	}
}

func TestCollectStreamsThePackedOutputUnderTheCap(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	engine.get(step("collect")).files["/work/collect/site.tar.gz"] = []byte("packed-bytes")
	var out bytes.Buffer
	if err := runner.Collect(context.Background(), testBuild, &out, 1024); err != nil || out.String() != "packed-bytes" {
		t.Fatalf("Collect: %v %q", err, out.String())
	}
	out.Reset()
	if err := runner.Collect(context.Background(), testBuild, &out, 4); !errors.Is(err, sitebuilds.ErrOutputTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
}

func TestLogsKeepEachStartedStepUnderItsHeader(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	engine.get(step("egress")).logs = "refused CONNECT 10.0.0.5:443\n"
	engine.get(step("prepare")).logs = ""
	engine.finish(step("prepare"), 0, false)
	mustStatus(t, runner)
	engine.get(step("clone")).logs = "checked out abc123\n"
	logs, err := runner.Logs(context.Background(), testBuild)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !strings.Contains(logs, "== network ==\nrefused CONNECT 10.0.0.5:443") || !strings.Contains(logs, "== clone ==\nchecked out abc123") {
		t.Fatalf("logs %q", logs)
	}
	if strings.Contains(logs, "== build ==") {
		t.Fatalf("a step that never started has a header: %q", logs)
	}
	if strings.Index(logs, "== network ==") > strings.Index(logs, "== clone ==") {
		t.Fatalf("steps out of order: %q", logs)
	}
}

func TestRemoveLeavesNothingButTheSharedNetwork(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	if err := runner.Remove(context.Background(), testBuild); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(engine.containers) != 0 || len(engine.volumes) != 0 {
		t.Fatalf("left %v %v", engine.containers, engine.volumes)
	}
	if _, ok := engine.networks[buildNetwork]; ok {
		t.Fatal("build network left")
	}
	if _, ok := engine.networks["excalibase-net-build-egress"]; !ok {
		t.Fatal("the shared egress network was removed")
	}
	if err := runner.Remove(context.Background(), testBuild); err != nil {
		t.Fatalf("a build already gone: %v", err)
	}
}

func TestStartClearsLeftoversOfTheSameBuild(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	startBuild(t, runner)
	if engine.get(step("prepare")).state != "running" {
		t.Fatal("restarted build did not start")
	}
}

func TestStartRefusesABadBuild(t *testing.T) {
	runner, _ := newTestRunner(t, newFakeEngine(), "runsc")
	for name, change := range map[string]func(*sitebuilds.RunSpec){
		"id":        func(s *sitebuilds.RunSpec) { s.BuildID = "../x" },
		"memory":    func(s *sitebuilds.RunSpec) { s.Memory = "lots" },
		"disk":      func(s *sitebuilds.RunSpec) { s.Disk = "" },
		"cpu":       func(s *sitebuilds.RunSpec) { s.CPUMillis = 0 },
		"time":      func(s *sitebuilds.RunSpec) { s.Timeout = 0 },
		"image":     func(s *sitebuilds.RunSpec) { s.Image = "" },
		"no memory": func(s *sitebuilds.RunSpec) { s.Memory = "0" },
		"no disk":   func(s *sitebuilds.RunSpec) { s.Disk = "0Gi" },
	} {
		spec := testSpec()
		change(&spec)
		if err := runner.Start(context.Background(), spec); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestPodmanBuildNetworksSkipDockersGatewayMode(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunnerOn(t, engine, "runsc", true)
	if err := runner.Verify(context.Background()); err != nil {
		t.Fatalf("Verify on Podman: %v", err)
	}
	for _, options := range engine.networkOptionsSeen {
		if _, set := options["com.docker.network.bridge.gateway_mode_ipv4"]; set {
			t.Fatal("the isolation check sent Podman Docker's gateway mode")
		}
	}
	startBuild(t, runner)
	if _, set := engine.networks[buildNetwork].Options["com.docker.network.bridge.gateway_mode_ipv4"]; set {
		t.Fatal("Podman was sent Docker's gateway mode")
	}
	if engine.networks["excalibase-net-build-egress"].Options["isolate"] != "true" {
		t.Fatal("the Podman egress bridge is not isolated")
	}
}

// The internal network keeps builds off the host only where the engine gives its bridge no host address.
func TestVerifyFindsWhetherBuildsReachTheHost(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	if err := runner.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !runner.HostIsolated() {
		t.Fatal("Docker 28+ isolates the host")
	}
	if len(engine.networks) != 0 {
		t.Fatalf("the check left %v", engine.networks)
	}
	older := newFakeEngine()
	older.ignoresGatewayMode = true
	runner, _ = newTestRunner(t, older, "runsc")
	if err := runner.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if runner.HostIsolated() {
		t.Fatal("an engine that gives the bridge a host address was counted as isolated")
	}
}

func TestStartNamesAnImageThatDidNotPull(t *testing.T) {
	engine := newFakeEngine()
	engine.pullMessage = "manifest unknown"
	runner, _ := newTestRunner(t, engine, "runsc")
	err := runner.Start(context.Background(), testSpec())
	if !errors.Is(err, sitebuilds.ErrImagePull) || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("Start: %v", err)
	}
}

func TestStartFailsWhenAStepCannotBeCreated(t *testing.T) {
	for _, name := range []string{"egress", "build"} {
		engine := newFakeEngine()
		engine.failCreate = step(name)
		runner, _ := newTestRunner(t, engine, "runsc")
		if err := runner.Start(context.Background(), testSpec()); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestCollectWithoutPackedOutputFails(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	if err := runner.Collect(context.Background(), testBuild, &bytes.Buffer{}, 1024); err == nil {
		t.Fatal("collected output that was never packed")
	}
}

func TestTailWriterKeepsTheEnd(t *testing.T) {
	tail := &tailWriter{limit: 8}
	for _, chunk := range []string{"0123456789", "abcdefghij", "KLMNOPQRST"} {
		_, _ = tail.Write([]byte(chunk))
	}
	if got := tail.String(); got != "…MNOPQRST" {
		t.Fatalf("tail %q", got)
	}
	short := &tailWriter{limit: 8}
	_, _ = short.Write([]byte("abc"))
	if got := short.String(); got != "abc" {
		t.Fatalf("short tail %q", got)
	}
}

func TestCappedWriterRefusesPastItsLimit(t *testing.T) {
	var out bytes.Buffer
	capped := &cappedWriter{out: &out, limit: 4}
	if _, err := capped.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := capped.Write([]byte("de")); !errors.Is(err, sitebuilds.ErrOutputTooLarge) || !capped.overflowed {
		t.Fatalf("past the cap: %v", err)
	}
}

// Output packed just before the deadline is still collected.
func TestStatusPrefersPackedOutputOverTheDeadline(t *testing.T) {
	engine := newFakeEngine()
	runner, now := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	for _, name := range []string{"prepare", "clone", "build"} {
		engine.finish(step(name), 0, false)
		mustStatus(t, runner)
	}
	engine.get(step("collect")).files["/work/collect/ready"] = []byte{}
	now.now = now.now.Add(11 * time.Minute)
	if status := mustStatus(t, runner); status.Phase != sitebuilds.RunReady {
		t.Fatalf("%+v", status)
	}
}

func TestStatusNamesAStepThatCannotStart(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	engine.failStart = step("clone")
	engine.finish(step("prepare"), 0, false)
	status := mustStatus(t, runner)
	if status.Phase != sitebuilds.RunFailed || status.Failure != sitebuilds.FailPlatform || strings.Contains(status.Detail, "runtime") {
		t.Fatalf("%+v", status)
	}
}

func TestStatusFailsWhenTheNetworkProxyStops(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	engine.finish(step("prepare"), 0, false)
	mustStatus(t, runner) // starts clone
	engine.finish(step("egress"), 2, false)
	status := mustStatus(t, runner)
	if status.Phase != sitebuilds.RunFailed || status.Failure != sitebuilds.FailPlatform || !strings.Contains(status.Detail, "proxy") {
		t.Fatalf("%+v", status)
	}
}

// A step the engine killed at the time limit is a timeout, not running out of memory.
func TestStatusNamesAnEngineKillAtTheDeadlineATimeout(t *testing.T) {
	engine := newFakeEngine()
	runner, now := newTestRunner(t, engine, "runsc")
	startBuild(t, runner)
	for _, name := range []string{"prepare", "clone"} {
		engine.finish(step(name), 0, false)
		mustStatus(t, runner)
	}
	engine.finish(step("build"), 137, false)
	now.now = now.now.Add(11 * time.Minute)
	if status := mustStatus(t, runner); status.Failure != sitebuilds.FailTimeout {
		t.Fatalf("%+v", status)
	}
}

// The sandbox carries no site in it: another kind of build (EXC-677 app builds) gets the same network,
// sidecar, watcher, labels and time limit with its own steps, and is cleaned up by its own step names.
func TestABoxServesBuildsOtherThanSites(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	app := runner.newBox(testBuild, "app-build", map[string]string{"excalibase.app": "web"}, 5*time.Minute, resource.MustParse("3Gi"))
	proxy, err := runner.openBox(context.Background(), app)
	if err != nil || proxy != "http://10.250.0.2:3128" {
		t.Fatalf("open: %q %v", proxy, err)
	}
	planned := runner.boxStep(app, "moby/buildkit:rootless", 2000, 4<<30, 4096, true)
	planned.config.Cmd = app.deadlined("buildctl-daemonless.sh", "build")
	if err := runner.create(context.Background(), testBuild, "image", planned); err != nil {
		t.Fatal(err)
	}
	image := engine.get(step("image"))
	if image.host.Runtime != "runsc" || string(image.host.NetworkMode) != buildNetwork || image.config.Labels["excalibase.component"] != "app-build" ||
		image.config.Labels["excalibase.app"] != "web" || image.config.Cmd[3] != "300" {
		t.Fatalf("app step %+v %+v", image.host, image.config)
	}
	if watch := strings.Join(engine.get(step("watch")).config.Cmd, " "); !strings.Contains(watch, "3145728") {
		t.Fatalf("watcher %s", watch)
	}
	if err := runner.removeBox(context.Background(), testBuild, []string{"image"}); err != nil {
		t.Fatal(err)
	}
	if engine.get(step("image")) != nil || engine.get(step("egress")) != nil || engine.volumes[workVolume] {
		t.Fatal("the box was not cleaned up")
	}
}

// Docker 27 refuses the isolated gateway with a 400: Verify still passes, says the host is reachable,
// and builds are laid out without the option instead of failing.
func TestAnEngineThatRefusesTheIsolatedGatewayStillBuilds(t *testing.T) {
	engine := newFakeEngine()
	engine.refusesGatewayMode = true
	runner, _ := newTestRunner(t, engine, "runsc")
	if err := runner.Verify(context.Background()); err != nil {
		t.Fatalf("Verify on Docker 27: %v", err)
	}
	if runner.HostIsolated() {
		t.Fatal("Docker 27 leaves builds a host address; Verify must say so")
	}
	startBuild(t, runner)
	options := engine.networks[buildNetwork]
	if !options.Internal || len(options.Options) != 0 {
		t.Fatalf("build network %+v: internal, without the refused option", options)
	}
}

// On Docker 28+ both address families get the isolated gateway, as project networks do (EXC-676).
func TestBuildNetworksIsolateBothAddressFamilies(t *testing.T) {
	engine := newFakeEngine()
	runner, _ := newTestRunner(t, engine, "runsc")
	if err := runner.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	startBuild(t, runner)
	options := engine.networks[buildNetwork].Options
	if options["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" || options["com.docker.network.bridge.gateway_mode_ipv6"] != "isolated" {
		t.Fatalf("build network options %v", options)
	}
}
