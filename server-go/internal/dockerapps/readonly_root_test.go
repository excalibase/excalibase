package dockerapps

import (
	"context"
	"encoding/json"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/excalibase/provisioning-poc/internal/engineproxy"
	"slices"
	"strings"
	"testing"
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
	if strings.Contains(opts, "noexec") {
		t.Fatalf("/tmp tmpfs options %q: frameworks unpack and run helpers from /tmp", opts)
	}
	if len(c.host.Tmpfs) != 1 {
		t.Fatalf("tmpfs mounts %v, want /tmp only", c.host.Tmpfs)
	}
}

// A copy into a read-only root is refused by the engine, so the probe goes into a volume of its own.
func TestProbeLivesInAVolumeBecauseTheRootIsReadOnly(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")
	c := h.engine.appContainers("app-01")[0]
	if _, ok := c.config.Volumes["/"+probeDir]; !ok {
		t.Fatalf("no volume at /%s: %v", probeDir, c.config.Volumes)
	}
	if string(c.files[probePath]) != "probe" {
		t.Fatalf("probe not copied in: %v", c.files)
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

// An image's own VOLUME is writable and uncapped whatever the root is; say so at deploy time.
func TestDeployWarnsWhenTheImageDeclaresItsOwnVolume(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.imageVolumes = []string{"/var/lib/app"}
	var logged []string
	h.rt.warn = func(format string, args ...any) { logged = append(logged, format) }
	h.deploy(freeApp(), "dep-1")
	if !slices.ContainsFunc(logged, func(line string) bool { return strings.Contains(line, "VOLUME") }) {
		t.Fatalf("no warning about the image's volume: %v", logged)
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
}
