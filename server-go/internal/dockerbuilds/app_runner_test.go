package dockerbuilds

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"github.com/docker/docker/api/types/mount"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const (
	testBuildKit = "moby/buildkit:v0.34.0-rootless"
	testBuilder  = "paketobuildpacks/ubuntu-noble-builder:0.0.203"
	testRunImage = "docker.io/paketobuildpacks/ubuntu-noble-run:0.0.150@sha256:feed"
	testCrane    = "gcr.io/go-containerregistry/crane:v0.20.6"
	testProxy    = "http://10.250.0.2:3128"
)

func testAppOptions() AppOptions {
	return AppOptions{ToolImage: testNode, BuildKitImage: testBuildKit, BuilderImage: testBuilder, RunImage: testRunImage, CraneImage: testCrane}
}

func newTestAppRunner(t *testing.T, engine *fakeEngine, sandbox string, podman bool) *AppRunner {
	t.Helper()
	runner, _ := newTestRunnerOn(t, engine, sandbox, podman)
	app, err := NewAppRunner(runner, testAppOptions())
	if err != nil {
		t.Fatalf("NewAppRunner: %v", err)
	}
	return app
}

func testAppSpec(builder appbuilds.Builder) appbuilds.RunSpec {
	return appbuilds.RunSpec{
		BuildID: testBuild, AppID: "app-1", ProjectID: "proj-1", Builder: builder,
		CloneURL: "https://github.com/acme/api.git", Commit: strings.Repeat("a", 40), CloneToken: testToken,
		RootDir: "api", BuildArgs: map[string]string{"NODE_ENV": "production", "HTTP_PROXY": "http://evil"},
		Secrets: map[string]string{"NPM_TOKEN": testSecret}, CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi",
		Timeout: 15 * time.Minute, MaxImageBytes: 2 << 30,
		LargestPlan: sitebuilds.BuildSize{CPUMillis: 4000, Memory: "8Gi", Disk: "20Gi"},
	}
}

func startApp(t *testing.T, app *AppRunner, builder appbuilds.Builder) {
	t.Helper()
	if err := app.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := app.Start(context.Background(), testAppSpec(builder)); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func appStatus(t *testing.T, app *AppRunner) sitebuilds.RunStatus {
	t.Helper()
	status, err := app.Status(context.Background(), testBuild)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return status
}

func TestADockerfileBuildRunsBuildKitInGVisorWithTheTwoCapabilitiesItNeeds(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderDockerfile)
	kit := engine.get(step("buildkit"))
	if kit == nil {
		t.Fatal("no buildkit step")
	}
	args := strings.Join(kit.config.Cmd, " ")
	switch {
	case kit.host.Runtime != "runsc" || !kit.host.ReadonlyRootfs:
		t.Fatalf("runtime %q readonly %v", kit.host.Runtime, kit.host.ReadonlyRootfs)
	case slices.Contains(kit.host.SecurityOpt, "no-new-privileges"):
		t.Fatalf("the buildkit step keeps no-new-privileges: %v", kit.host.SecurityOpt)
	case !slices.Equal(kit.host.CapDrop, []string{"ALL"}) || !slices.Equal(kit.host.CapAdd, []string{"SETUID", "SETGID"}):
		t.Fatalf("caps drop %v add %v", kit.host.CapDrop, kit.host.CapAdd)
	case !strings.Contains(args, "--opt build-arg:HTTP_PROXY="+testProxy) || strings.Contains(args, "evil"):
		t.Fatalf("the proxy is not the build's HTTP_PROXY argument: %s", args)
	case !strings.Contains(args, "build-arg:NODE_ENV=production") || !strings.Contains(args, "type=oci,dest=/work/out/image.tar"):
		t.Fatalf("args %s", args)
	case kit.config.Image != testBuildKit || env(kit)["HTTP_PROXY"] != testProxy || env(kit)["BUILDKITD_FLAGS"] == "":
		t.Fatalf("image %q env %v", kit.config.Image, env(kit))
	case !slices.Contains(engine.pulled, testBuildKit):
		t.Fatalf("pulled %v", engine.pulled)
	}
}

func TestOnlyTheBuildKitStepIsLooserThanTheRest(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderDockerfile)
	for _, name := range []string{"prepare", "clone", "collect", "egress", "watch"} {
		c := engine.get(step(name))
		if !slices.Contains(c.host.SecurityOpt, "no-new-privileges") || len(c.host.CapAdd) != 0 || !slices.Equal(c.host.CapDrop, []string{"ALL"}) || !c.host.ReadonlyRootfs {
			t.Errorf("%s: %+v", name, c.host)
		}
	}
	if engine.get(step("clone")).host.Runtime != "runsc" || engine.get(step("collect")).host.Runtime != "runsc" {
		t.Error("a step is outside gVisor")
	}
}

func TestADockerfileBuildLaysOutItsStepsAndTheSecretsStayOffTheCommandLine(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderDockerfile)
	for _, name := range []string{"prepare", "clone", "buildkit", "collect", "egress", "watch"} {
		if engine.get(step(name)) == nil {
			t.Errorf("no %s step", name)
		}
	}
	if engine.get(step("analyze")) != nil || engine.get(step("buildpacks")) != nil {
		t.Error("a buildpacks step in a Dockerfile build")
	}
	kit := engine.get(step("buildkit"))
	entry := strings.Join(kit.config.Entrypoint, " ")
	if !strings.Contains(entry, "buildctl-daemonless.sh") || !strings.Contains(entry, "/work/src/api") || strings.Contains(entry, testSecret) {
		t.Fatalf("entry %s", entry)
	}
	if !strings.Contains(strings.Join(kit.config.Cmd, " "), "id=NPM_TOKEN,src=/run/build-secrets/NPM_TOKEN") || env(kit)["EXC_BUILDVAR_NPM_TOKEN"] != testSecret {
		t.Fatalf("secret not delivered as a file: %v", kit.config.Cmd)
	}
	if _, ok := kit.host.Tmpfs["/run/build-secrets"]; !ok {
		t.Fatalf("no tmpfs for the secret files: %v", kit.host.Tmpfs)
	}
	if engine.get(step("clone")).config.Env == nil || strings.Contains(strings.Join(kit.config.Env, " "), testToken) {
		t.Fatal("the git token reached the build step")
	}
	if got := engine.get(step("clone")).config.Image; got != testNode {
		t.Fatalf("clone image %q", got)
	}
}

func assertBuildpackPhasesLockedDown(t *testing.T, engine *fakeEngine) {
	t.Helper()
	online := map[string]bool{"run-image": true, "buildpacks": true}
	for _, name := range []string{"share-checkout", "run-image", "analyze", "buildpacks", "export", "collect"} {
		c := engine.get(step(name))
		if c == nil {
			t.Fatalf("no %s step", name)
		}
		if !slices.Contains(c.host.SecurityOpt, "no-new-privileges") || len(c.host.CapAdd) != 0 || !c.host.ReadonlyRootfs || c.host.Runtime != "runsc" {
			t.Errorf("%s: %+v", name, c.host)
		}
		if got := string(c.host.NetworkMode) != "none"; got != online[name] {
			t.Errorf("%s: online %v", name, got)
		}
	}
	for _, name := range []string{"run-image", "analyze", "buildpacks", "export", "collect"} {
		if user := engine.get(step(name)).config.User; user != "1001:1001" {
			t.Errorf("%s runs as %q", name, user)
		}
	}
}

func assertBuildpackEnvAndImages(t *testing.T, engine *fakeEngine) {
	t.Helper()
	buildpacks := engine.get(step("buildpacks"))
	if env(buildpacks)["HTTPS_PROXY"] != testProxy || env(buildpacks)["EXC_BUILDVAR_NPM_TOKEN"] != testSecret || env(buildpacks)["APP_DIR"] != "/work/src/api" {
		t.Fatalf("env %v", env(buildpacks))
	}
	if env(engine.get(step("analyze")))["EXC_BUILDVAR_NPM_TOKEN"] != "" {
		t.Fatal("secrets reached a step that runs no app code")
	}
	if !slices.Contains(engine.pulled, testBuilder) || !slices.Contains(engine.pulled, testCrane) {
		t.Fatalf("pulled %v", engine.pulled)
	}
}

func TestBuildpacksRunEveryPhaseLockedDownAsTheCNBUser(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderBuildpacks)
	if engine.get(step("buildkit")) != nil {
		t.Fatal("a buildkit step in a buildpacks build")
	}
	assertBuildpackPhasesLockedDown(t, engine)
	assertBuildpackEnvAndImages(t, engine)
}

func TestADockerfileBuildIsRefusedWithoutGVisorByName(t *testing.T) {
	for name, tc := range map[string]struct {
		sandbox string
		podman  bool
	}{"no runtime": {"", false}, "podman": {"runsc", true}} {
		engine := newFakeEngine()
		app := newTestAppRunner(t, engine, tc.sandbox, tc.podman)
		err := app.Start(context.Background(), testAppSpec(appbuilds.BuilderDockerfile))
		if !errors.Is(err, ErrDockerfileBuildsNeedGVisor) || len(engine.containers) != 0 || len(engine.pulled) != 0 {
			t.Fatalf("%s: err %v, containers %d", name, err, len(engine.containers))
		}
		if !errors.Is(app.Supports(appbuilds.BuilderDockerfile), ErrDockerfileBuildsNeedGVisor) {
			t.Fatalf("%s: Supports allowed a Dockerfile build", name)
		}
	}
}

func TestBuildpacksAreAllowedWithoutGVisor(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "", false)
	if err := app.Supports(appbuilds.BuilderBuildpacks); err != nil {
		t.Fatalf("Supports: %v", err)
	}
	startApp(t, app, appbuilds.BuilderBuildpacks)
	if engine.get(step("buildpacks")).host.Runtime != "" {
		t.Fatal("a runtime was set the operator opted out of")
	}
}

func TestAnAppBuildRefusesWhatTheSpecRefuses(t *testing.T) {
	app := newTestAppRunner(t, newFakeEngine(), "runsc", false)
	spec := testAppSpec(appbuilds.BuilderDockerfile)
	spec.Commit = "main"
	if err := app.Start(context.Background(), spec); err == nil {
		t.Fatal("a branch name was accepted as a commit")
	}
}

func TestADockerfileBuildRunsItsStepsInOrderAndNamesFailures(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderDockerfile)
	for _, name := range []string{"prepare", "clone"} {
		engine.finish(step(name), 0, false)
		appStatus(t, app)
	}
	if engine.get(step("buildkit")).state != "running" || engine.get(step("collect")).state != "created" {
		t.Fatal("buildkit did not start after clone")
	}
	engine.finish(step("buildkit"), 12, false)
	status := appStatus(t, app)
	if status.Failure != appbuilds.FailNoDockerfile || status.ExitCode != 12 {
		t.Fatalf("%+v", status)
	}
}

func TestABuildKitStepKilledIsOutOfMemory(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderDockerfile)
	engine.finish(step("prepare"), 0, false)
	appStatus(t, app)
	engine.finish(step("clone"), 0, false)
	appStatus(t, app)
	engine.finish(step("buildkit"), 137, false)
	if status := appStatus(t, app); status.Failure != sitebuilds.FailOutOfMemory {
		t.Fatalf("%+v", status)
	}
}

func TestBuildpacksPhasesRunInOrderAndANoBuildpackExitIsNamed(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderBuildpacks)
	order := []string{"prepare", "clone", "share-checkout", "run-image", "analyze"}
	for i, name := range order {
		engine.finish(step(name), 0, false)
		appStatus(t, app)
		if next := []string{"clone", "share-checkout", "run-image", "analyze", "buildpacks"}[i]; engine.get(step(next)).state != "running" {
			t.Fatalf("%s did not start after %s", next, name)
		}
	}
	engine.finish(step("buildpacks"), 20, false)
	if status := appStatus(t, app); status.Failure != appbuilds.FailNoBuildpack {
		t.Fatalf("%+v", status)
	}
}

func TestAnAppBuildIsReadyOnceCollectWroteItsTokenAndYieldsTheArchive(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderDockerfile)
	for _, name := range []string{"prepare", "clone", "buildkit"} {
		engine.finish(step(name), 0, false)
		appStatus(t, app)
	}
	collect := engine.get(step("collect"))
	collect.files["/work/collect/ready"] = []byte(collect.config.Labels[labelReady])
	collect.files["/work/collect/image.tar"] = []byte("oci-archive")
	if status := appStatus(t, app); status.Phase != sitebuilds.RunReady {
		t.Fatalf("%+v", status)
	}
	var out bytes.Buffer
	if err := app.Collect(context.Background(), testBuild, &out, 1<<20); err != nil || out.String() != "oci-archive" {
		t.Fatalf("collect %q %v", out.String(), err)
	}
	if err := app.Collect(context.Background(), testBuild, &out, 3); !errors.Is(err, sitebuilds.ErrOutputTooLarge) {
		t.Fatalf("collect past the limit: %v", err)
	}
}

func TestAnAppBuildsLogsAndRemoveCoverEveryStep(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderBuildpacks)
	engine.get(step("clone")).logs = "checked out"
	engine.get(step("clone")).state = "exited"
	engine.get(step("analyze")).state = "exited"
	engine.get(step("analyze")).logs = "analyzed"
	logs, err := app.Logs(context.Background(), testBuild)
	if err != nil || !strings.Contains(logs, "== clone ==\nchecked out") || !strings.Contains(logs, "== analyze ==\nanalyzed") {
		t.Fatalf("logs %q %v", logs, err)
	}
	if err := app.Remove(context.Background(), testBuild); err != nil {
		t.Fatal(err)
	}
	if len(engine.containers) != 0 || len(engine.volumes) != 0 || len(engine.networks) != 1 {
		t.Fatalf("left behind: %d containers, %v volumes, %v networks", len(engine.containers), engine.volumes, engine.networks)
	}
}

// A blocked registry (ttl.sh) must fail to resolve inside every app step, as the app build pod pins it to loopback.
func TestEveryAppStepPinsTheBlockedRegistriesToLoopback(t *testing.T) {
	for _, builder := range []appbuilds.Builder{appbuilds.BuilderDockerfile, appbuilds.BuilderBuildpacks} {
		engine := newFakeEngine()
		app := newTestAppRunner(t, engine, "runsc", false)
		startApp(t, app, builder)
		for _, name := range planFor(builder).chain {
			created := engine.get(step(name))
			if created == nil {
				t.Fatalf("%s: no %s step", builder, name)
			}
			for _, pin := range []string{"ttl.sh:127.0.0.1", "ttl.sh:::1"} {
				if !slices.Contains(created.host.ExtraHosts, pin) {
					t.Errorf("%s step %s hosts %v: no %s", builder, name, created.host.ExtraHosts, pin)
				}
			}
		}
	}
}

func TestADockerfileBuildNeedsARuntimeThatIsRunscNotJustNamedSo(t *testing.T) {
	for name, tc := range map[string]struct {
		runtime string
		paths   map[string]string
		allowed bool
	}{
		"runc":               {"runc", nil, false},
		"crun":               {"crun", nil, false},
		"runsc by name only": {"runsc", map[string]string{"runsc": "/usr/bin/runc"}, false},
		"runsc lookalike":    {"runsc", map[string]string{"runsc": "/opt/notrunsc"}, false},
		"real runsc":         {"runsc", map[string]string{"runsc": "/usr/local/bin/runsc"}, true},
		"renamed runsc":      {"gvisor", map[string]string{"gvisor": "/opt/gvisor/bin/runsc"}, true},
	} {
		engine := newFakeEngine()
		engine.runtimes = append(engine.runtimes, tc.runtime)
		engine.runtimePaths = tc.paths
		app := newTestAppRunner(t, engine, tc.runtime, false)
		if err := app.Verify(context.Background()); err != nil {
			t.Fatalf("%s: Verify: %v", name, err)
		}
		err := app.Start(context.Background(), testAppSpec(appbuilds.BuilderDockerfile))
		if tc.allowed != (err == nil) || (!tc.allowed && !errors.Is(err, ErrDockerfileBuildsNeedGVisor)) {
			t.Errorf("%s: Start answered %v", name, err)
		}
		if tc.allowed != (app.Supports(appbuilds.BuilderDockerfile) == nil) {
			t.Errorf("%s: Supports disagrees with Start", name)
		}
	}
}

func TestADockerfileBuildIsRefusedUntilVerifyFoundRunsc(t *testing.T) {
	engine := newFakeEngine()
	engine.runtimePaths = map[string]string{"runsc": "/usr/local/bin/runsc"}
	app := newTestAppRunner(t, engine, "runsc", false)
	if !errors.Is(app.Supports(appbuilds.BuilderDockerfile), ErrDockerfileBuildsNeedGVisor) {
		t.Fatal("Supports allowed a Dockerfile build before Verify")
	}
}

func TestVerifyFailsForAMissingRuntime(t *testing.T) {
	engine := newFakeEngine()
	engine.runtimes = []string{"runc"}
	app := newTestAppRunner(t, engine, "runsc", false)
	if err := app.Verify(context.Background()); err == nil {
		t.Fatal("Verify passed with no runsc runtime")
	}
}

// Every step of both builders, the sidecar and the disk watcher included, gets no host namespace, device or mount.
func TestNoAppStepGetsPrivilegeHostNamespacesDevicesOrMounts(t *testing.T) {
	for _, builder := range []appbuilds.Builder{appbuilds.BuilderDockerfile, appbuilds.BuilderBuildpacks} {
		engine := newFakeEngine()
		engine.runtimePaths = map[string]string{"runsc": "/usr/local/bin/runsc"}
		app := newTestAppRunner(t, engine, "runsc", false)
		startApp(t, app, builder)
		names := append([]string{"egress", "watch"}, planFor(builder).chain...)
		for _, name := range names {
			c := engine.get(step(name))
			if c == nil {
				t.Fatalf("%s: no %s step", builder, name)
			}
			host := c.host
			where := string(builder) + "/" + name
			switch {
			case host.Privileged:
				t.Errorf("%s is privileged", where)
			case host.PidMode != "" || host.IpcMode != "" || host.UTSMode != "" || host.UsernsMode != "" || host.CgroupnsMode != "":
				t.Errorf("%s shares a namespace: pid %q ipc %q uts %q userns %q cgroupns %q", where, host.PidMode, host.IpcMode, host.UTSMode, host.UsernsMode, host.CgroupnsMode)
			case len(host.Binds) != 0 || len(host.Devices) != 0 || len(host.Resources.DeviceCgroupRules) != 0 || len(host.DeviceRequests) != 0:
				t.Errorf("%s has binds %v or devices %v", where, host.Binds, host.Devices)
			case string(host.NetworkMode) == "host" || strings.HasPrefix(string(host.NetworkMode), "container:"):
				t.Errorf("%s joins network %q", where, host.NetworkMode)
			}
			for _, option := range host.SecurityOpt {
				if strings.Contains(option, "unconfined") {
					t.Errorf("%s has %s", where, option)
				}
			}
			for _, m := range host.Mounts {
				if m.Type != mount.TypeVolume || m.Target != workDir || m.Source != app.workVolume(testBuild) {
					t.Errorf("%s mounts %+v, beyond the work volume", where, m)
				}
			}
		}
	}
}

func TestTheBuildpacksStepKeepsItsPlatformEnvOnANoExecTmpfs(t *testing.T) {
	engine := newFakeEngine()
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderBuildpacks)
	options, ok := engine.get(step("buildpacks")).host.Tmpfs[appPlatformEnv]
	if !ok || !strings.Contains(options, "noexec") || !strings.Contains(options, "nosuid") || !strings.Contains(options, "nodev") {
		t.Fatalf("platform env tmpfs %q (present %v)", options, ok)
	}
	if _, leaked := engine.get(step("analyze")).host.Tmpfs[appPlatformEnv]; leaked {
		t.Fatal("a step that does not run the app's code has its secrets")
	}
}

func TestCollectRefusesAFirstEntryThatIsNotARegularFile(t *testing.T) {
	for name, kind := range map[string]byte{"symlink": tar.TypeSymlink, "directory": tar.TypeDir} {
		engine := newFakeEngine()
		engine.tarType = kind
		app := newTestAppRunner(t, engine, "runsc", false)
		startApp(t, app, appbuilds.BuilderDockerfile)
		engine.get(step("collect")).files["/work/collect/image.tar"] = []byte("oci-archive")
		var out bytes.Buffer
		if err := app.Collect(context.Background(), testBuild, &out, 1<<20); err == nil || out.Len() != 0 {
			t.Errorf("%s: collect answered %v after writing %d bytes", name, err, out.Len())
		}
	}
}

// Read a byte at a time, the capped writer alone would pass bytes on before it overflowed: the header's size must refuse first.
func TestCollectRefusesADeclaredSizePastTheLimitBeforeWritingAnything(t *testing.T) {
	engine := newFakeEngine()
	engine.oneByte = true
	app := newTestAppRunner(t, engine, "runsc", false)
	startApp(t, app, appbuilds.BuilderDockerfile)
	engine.get(step("collect")).files["/work/collect/image.tar"] = []byte("oci-archive")
	var out bytes.Buffer
	if err := app.Collect(context.Background(), testBuild, &out, 8); !errors.Is(err, sitebuilds.ErrOutputTooLarge) || out.Len() != 0 {
		t.Fatalf("collect: %v after writing %q", err, out.String())
	}
}

func TestDeclaredBuildArgumentsNamingAProxyAreDroppedWhateverTheirCase(t *testing.T) {
	declared := map[string]string{"NODE_ENV": "production", "Http_Proxy": "x", "HTTPS_PROXY": "x", "all_proxy": "x", "ALL_PROXY": "x",
		"ftp_proxy": "x", "FTP_PROXY": "x", "no_proxy": "x", "NO_PROXY": "x", "npm_config_proxy": "x", "YARN_HTTPS_PROXY": "x"}
	kept := withoutProxyArgs(declared)
	if len(kept) != 1 || kept["NODE_ENV"] != "production" {
		t.Fatalf("kept %v", kept)
	}
}
