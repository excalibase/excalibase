package dockerapps

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/engineproxy"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-732: an app's root filesystem is read-only, so nothing it writes can fill the host's
// disk through the engine's writable layer, on any storage engine and under gVisor alike.
// What it may write is /tmp, capped by a share of its own memory limit, and its disk.
func TestAppRootIsReadOnlyWithACappedTmp(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")
	c := h.engine.appContainers("app-01")[0]

	if !c.host.ReadonlyRootfs {
		t.Fatal("the app's root filesystem is writable")
	}
	opts := c.host.Tmpfs["/tmp"]
	// The harness app is limited to 256 MiB of memory; the default share is a quarter.
	want := "size=" + "67108864"
	if !strings.Contains(opts, want) || !strings.Contains(opts, "mode=1777") || !strings.Contains(opts, "nosuid") || !strings.Contains(opts, "nodev") {
		t.Fatalf("/tmp tmpfs options %q, want %s, mode=1777, nosuid, nodev", opts, want)
	}
	if strings.Contains(opts, "noexec") || !slices.Contains(strings.Split(opts, ","), "exec") {
		t.Fatalf("/tmp tmpfs options %q: the engine adds noexec unless exec is passed, and runtimes unpack and run helpers from /tmp", opts)
	}
	if len(c.host.Tmpfs) != 1 {
		t.Fatalf("tmpfs mounts %v, want /tmp only", c.host.Tmpfs)
	}
}

// The probe lives in a volume of the platform's own, mounted read-only into the app: a path the
// app could write would be a disk it can fill.
func TestProbeIsMountedReadOnlyFromThePlatformsVolume(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")
	c := h.engine.appContainers("app-01")[0]
	var probe *mount.Mount
	for i, m := range c.host.Mounts {
		if m.Target == "/"+probeDir {
			probe = &c.host.Mounts[i]
		}
	}
	if probe == nil || probe.Type != mount.TypeVolume || !probe.ReadOnly {
		t.Fatalf("mounts %+v, want a read-only volume at /%s", c.host.Mounts, probeDir)
	}
	if !strings.HasPrefix(probe.Source, "excalibase-vol-probe-") {
		t.Fatalf("probe volume %q is not the platform's", probe.Source)
	}
	labels := h.engine.volumes[probe.Source]
	if labels[engineproxy.SharedReadOnlyLabel] != "true" || labels[labelManaged] != "true" {
		t.Fatalf("probe volume labels %v", labels)
	}
	if string(h.engine.volumeFiles[probe.Source]["/"+probeDir+"/probe"]) != "probe" {
		t.Fatalf("the probe is not in the volume: %v", h.engine.volumeFiles[probe.Source])
	}
	if len(c.files) != 0 || len(c.config.Volumes) != 0 {
		t.Fatalf("the app container itself was written to: files %v, volumes %v", c.files, c.config.Volumes)
	}
}

// One probe volume serves every app; a new probe or CA makes a new one and drops the old.
func TestProbeVolumeIsMadeOncePerContentAndStaleOnesGo(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")
	h.deploy(freeApp(), "dep-2")
	var made []string
	for _, event := range h.engine.calls {
		if strings.HasPrefix(event, "volume create excalibase-vol-probe-") {
			made = append(made, event)
		}
	}
	if len(made) != 1 {
		t.Fatalf("probe volumes made: %v, want one", made)
	}
	h.rt.opts.ProbeBinary = []byte("probe v2")
	h.deploy(freeApp(), "dep-3")
	var probes []string
	for name := range h.engine.volumes {
		if strings.HasPrefix(name, "excalibase-vol-probe-") {
			probes = append(probes, name)
		}
	}
	if len(probes) != 1 {
		t.Fatalf("probe volumes after a new probe: %v, want only the new one", probes)
	}
}

// The filler writes the volume, never starts, and is gone afterwards.
func TestProbeVolumeFillerNeverRunsAndIsRemoved(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")
	for _, event := range h.engine.calls {
		if strings.Contains(event, "-fill") && strings.HasPrefix(event, "start") {
			t.Fatalf("the filler was started: %s", event)
		}
	}
	for _, c := range h.engine.containers {
		if strings.HasSuffix(c.name, "-fill") {
			t.Fatalf("filler %s was left behind", c.name)
		}
	}
}

// A failed fill leaves no half-filled volume to be trusted by the next deploy.
func TestFailedProbeFillRemovesTheVolume(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.failCopy = true
	if _, err := h.tryDeploy(freeApp(), "dep-1"); err == nil {
		t.Fatal("the deploy survived a failed fill")
	}
	for name := range h.engine.volumes {
		if strings.HasPrefix(name, "excalibase-vol-probe-") {
			t.Fatalf("half-filled volume %s kept", name)
		}
	}
}

func TestTmpShareFollowsTheOperatorsPercentage(t *testing.T) {
	for percent, want := range map[int]string{10: "size=26843545", 50: "size=134217728", 100: "size=268435456"} {
		h := newHarness(t, func(o *Options) { o.TmpPercent = percent })
		h.deploy(freeApp(), "dep-1")
		if got := h.engine.appContainers("app-01")[0].host.Tmpfs["/tmp"]; !strings.Contains(got, want) {
			t.Errorf("%d%%: tmpfs options %q, want %s", percent, got, want)
		}
	}
}

// Removing an app's container removes the probe volume with it, or every deploy leaks one.
func TestRemovingAnAppContainerRemovesItsAnonymousVolumes(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")
	if err := h.rt.DeleteAppWorkload(context.Background(), testDB, "app-01", 0); err != nil {
		t.Fatal(err)
	}
	if len(h.engine.removeOptions) == 0 {
		t.Fatal("deleting the app removed no container")
	}
	for _, options := range h.engine.removeOptions {
		if !options.RemoveVolumes {
			t.Errorf("container removed without its volumes: %+v", options)
		}
	}
}

func TestTmpPercentIsValidated(t *testing.T) {
	for _, percent := range []int{-1, 101, 1000} {
		opts := testOptions(t.TempDir())
		opts.TmpPercent = percent
		if _, err := New(newFakeEngine(), opts); err == nil || !strings.Contains(err.Error(), "percent") {
			t.Errorf("TmpPercent %d: err = %v, want a refusal naming the percentage", percent, err)
		}
	}
	for _, percent := range []int{0, 1, 25, 100} {
		opts := testOptions(t.TempDir())
		opts.TmpPercent = percent
		if _, err := New(newFakeEngine(), opts); err != nil {
			t.Errorf("TmpPercent %d refused: %v", percent, err)
		}
	}
}

// An image's own VOLUME would be an anonymous, writable, uncapped host volume, so each declared
// path gets a capped tmpfs of its own that keeps the image's files; none is left to the engine.
func TestImageVolumesBecomeCappedTmpfs(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.TmpCopyUp = true })
	h.engine.imageVolumes = []string{"/var/lib/app", "/cache", "/cache/", "/tmp"}
	h.deploy(freeApp(), "dep-1")
	c := h.engine.appContainers("app-01")[0]
	// 25% of 256 MiB is shared by /tmp and the two volumes.
	share := "size=" + strconv.FormatInt(256*1024*1024*25/100/3, 10)
	for _, path := range []string{"/tmp", "/var/lib/app", "/cache"} {
		opts := c.host.Tmpfs[path]
		if !strings.Contains(opts, share) || !strings.Contains(opts, "exec") || strings.Contains(opts, "noexec") {
			t.Errorf("%s: tmpfs options %q, want %s and exec", path, opts, share)
		}
	}
	for _, path := range []string{"/var/lib/app", "/cache"} {
		if !strings.Contains(c.host.Tmpfs[path], "tmpcopyup") {
			t.Errorf("%s: %q does not keep the image's files (tmpcopyup)", path, c.host.Tmpfs[path])
		}
	}
	inspect, err := h.engine.ContainerInspect(context.Background(), c.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range inspect.Mounts {
		if strings.HasPrefix(m.Name, "anon-") {
			t.Errorf("an anonymous volume %s remains at %s", m.Name, m.Destination)
		}
	}
}

// The app's own disk keeps its path; it is not covered by a tmpfs.
// Docker rejects tmpcopyup, so it is sent only where the engine is Podman.
func TestDockerIsNotSentTmpcopyup(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.imageVolumes = []string{"/cache"}
	h.deploy(freeApp(), "dep-1")
	for path, opts := range h.engine.appContainers("app-01")[0].host.Tmpfs {
		if strings.Contains(opts, "tmpcopyup") {
			t.Errorf("%s: %q: Docker would refuse the whole create", path, opts)
		}
	}
}

func TestImageVolumeAtTheDiskMountKeepsTheDisk(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.imageVolumes = []string{"/data"}
	h.createDisk(diskApp())
	h.deploy(diskApp(), "dep-1")
	c := h.engine.appContainers("app-01")[0]
	if _, covered := c.host.Tmpfs["/data"]; covered {
		t.Fatalf("tmpfs %v shadows the app's disk at /data", c.host.Tmpfs)
	}
}

func TestImageInspectFailureRefusesTheDeploy(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.failImageInspect = true
	if _, err := h.tryDeploy(freeApp(), "dep-1"); err == nil {
		t.Fatal("an image whose volumes are unknown was run")
	}
}

// size=0 means unlimited to tmpfs: no memory limit, or a share that rounds to nothing, is refused.
func TestTmpSizeNeverRoundsToUnlimited(t *testing.T) {
	r := &Runtime{opts: Options{TmpPercent: 25}}
	if _, err := r.scratchMounts(containerSpec{MemoryBytes: 0}, nil); err == nil {
		t.Error("a zero memory limit produced a tmpfs")
	}
	if _, err := r.scratchMounts(containerSpec{MemoryBytes: 3}, nil); err == nil {
		t.Error("a share of 0 bytes produced a tmpfs")
	}
	if mounts, err := r.scratchMounts(containerSpec{MemoryBytes: 1 << 20}, nil); err != nil || strings.Contains(mounts["/tmp"], "size=0") {
		t.Errorf("1 MiB: %v %v", mounts, err)
	}
}

// App logs rotate so what an app prints cannot fill the host's disk.
func TestAppLogsRotate(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")
	logs := h.engine.appContainers("app-01")[0].host.LogConfig
	if logs.Type != "json-file" || logs.Config["max-size"] != "5m" || logs.Config["max-file"] != "3" {
		t.Fatalf("log config %+v, want json-file 5m x 3", logs)
	}
	custom := newHarness(t, func(o *Options) { o.LogMaxSize, o.LogMaxFiles = "1m", 2 })
	custom.deploy(freeApp(), "dep-1")
	logs = custom.engine.appContainers("app-01")[0].host.LogConfig
	if logs.Config["max-size"] != "1m" || logs.Config["max-file"] != "2" {
		t.Fatalf("custom log config %+v", logs)
	}
}

func TestLogRotationIsValidated(t *testing.T) {
	for _, bad := range []Options{{LogMaxSize: "lots"}, {LogMaxSize: "0m"}, {LogMaxFiles: -1}, {LogMaxFiles: 101}} {
		opts := testOptions(t.TempDir())
		opts.LogMaxSize, opts.LogMaxFiles = bad.LogMaxSize, bad.LogMaxFiles
		if _, err := New(newFakeEngine(), opts); err == nil || !strings.Contains(err.Error(), "log") {
			t.Errorf("%+v: err = %v, want a refusal naming the log rotation", bad, err)
		}
	}
}

// The engine proxy admits what an app is now created with: a read-only root, a tmpfs and an
// anonymous volume for the probe. A field it refused would fail every deploy on a real host.
func TestEngineProxyAdmitsTheReadOnlyAppCreate(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")
	c := h.engine.appContainers("app-01")[0]
	body, err := json.Marshal(struct {
		*container.Config
		HostConfig       *container.HostConfig
		NetworkingConfig *network.NetworkingConfig
	}{&c.config, &c.host, &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{testNet: {}}}})
	if err != nil {
		t.Fatal(err)
	}
	policy := engineproxy.Policy{
		ManagedLabel: labelManaged, NetworkPrefix: "excalibase-net-", VolumePrefix: "excalibase-vol-",
		Runtimes: []string{"runsc"}, ContainerPrefix: "excalibase-",
	}
	if err := policy.CheckCreate(c.name, body); err != nil {
		t.Fatalf("the proxy refuses the app's create: %v", err)
	}
	filler := h.engine.filler
	if filler == nil {
		t.Fatal("no probe filler was created")
	}
	body, err = json.Marshal(struct {
		*container.Config
		HostConfig *container.HostConfig
	}{&filler.config, &filler.host})
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.CheckCreate(filler.name, body); err != nil {
		t.Fatalf("the proxy refuses the probe filler's create: %v", err)
	}
}

// tryDeploy is deploy that returns the apply's error.
func (h *harness) tryDeploy(app *apphost.App, deployID string) (*k8s.AppWorkload, error) {
	h.t.Helper()
	opts := renderOptions()
	opts.DeployID = deployID
	workload, err := k8s.RenderAppWorkload(testDB, app, stubResolver{}, opts)
	if err != nil {
		h.t.Fatal(err)
	}
	return workload, h.rt.ApplyAppWorkload(context.Background(), testDB, workload)
}
