//go:build integration

package dockerapps

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-732 on a real engine through the real proxy: the app's root is read-only, /tmp is capped,
// the probe still reaches the container, and the probe's volume goes with it.
// Run: go test -tags=integration ./internal/dockerapps/ -run TestRealEngineAppRoot -v
func TestRealEngineAppRootIsReadOnlyWithCappedTmp(t *testing.T) {
	rig := newRealRig(t)
	ctx := context.Background()
	rig.deploy(itApp("one"), "dep-1")

	list, err := rig.direct.ContainerList(ctx, container.ListOptions{Filters: filters.NewArgs(filters.Arg("label", labelApp+"=app-it"))})
	if err != nil || len(list) != 1 {
		t.Fatalf("app containers %d (%v)", len(list), err)
	}
	id := list[0].ID
	inspect, err := rig.direct.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !inspect.HostConfig.ReadonlyRootfs {
		t.Fatal("the engine runs the app with a writable root")
	}
	if got := inspect.HostConfig.Tmpfs["/tmp"]; !strings.Contains(got, "size=") {
		t.Fatalf("/tmp options %q", got)
	}
	probeVolume := probeVolumeOf(t, inspect)
	if logs := inspect.HostConfig.LogConfig; logs.Type != "json-file" || logs.Config["max-size"] != "5m" || logs.Config["max-file"] != "3" {
		t.Fatalf("log config %+v", logs)
	}
	assertWritesInsideTheApp(t, rig, id)

	if err := rig.rt.DeleteAppWorkload(ctx, rig.db, "app-it", time.Minute); err != nil {
		t.Fatal(err)
	}
	// One probe volume serves every app, so it stays; no filler is left behind.
	if _, err := rig.direct.VolumeInspect(ctx, probeVolume); err != nil {
		t.Fatalf("the shared probe volume %s is gone: %v", probeVolume, err)
	}
	leftovers, err := rig.direct.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("name", "-fill"))})
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("probe fillers left behind: %d (%v)", len(leftovers), err)
	}
	_ = rig.direct.VolumeRemove(ctx, probeVolume, false)
}

// probeVolumeOf is the platform's probe volume the app mounts, which must not be writable.
func probeVolumeOf(t *testing.T, inspect container.InspectResponse) string {
	t.Helper()
	for _, m := range inspect.Mounts {
		if m.Destination != "/"+probeDir {
			continue
		}
		if m.RW {
			t.Fatalf("the probe volume is mounted writable: %+v", m)
		}
		if strings.Contains(m.Name, "-vol-probe-") {
			return m.Name
		}
	}
	t.Fatalf("no platform probe volume among %+v", inspect.Mounts)
	return ""
}

// assertWritesInsideTheApp runs what an app does to its filesystem and checks the engine's answer.
func assertWritesInsideTheApp(t *testing.T, rig *realRig, id string) {
	t.Helper()
	checks := []struct{ what, command, want string }{
		// Opus wrote 50 MB into the old writable probe path: it is refused now, and the probe is still readable.
		{"a write to the probe volume", "dd if=/dev/zero of=/.excalibase/filler bs=1M count=50", "Read-only"},
		{"the probe", "test -x /.excalibase/probe && echo probe-ok", "probe-ok"},
		// Docker adds noexec to a tmpfs unless exec is passed; runtimes run native helpers from /tmp.
		{"the /tmp mount", "grep ' /tmp ' /proc/mounts", "tmpfs"},
		{"a binary copied into /tmp", "cp /bin/busybox /tmp/echo && /tmp/echo ran", "ran"},
		{"a write to the root", "touch /rootfs-probe", "Read-only"},
		{"writes to /tmp and the disk", "touch /tmp/ok && touch /data/ok && echo wrote", "wrote"},
	}
	for _, check := range checks {
		if out := rig.exec(id, check.command); !strings.Contains(out, check.want) {
			t.Errorf("%s answered %q, want %q", check.what, out, check.want)
		}
	}
	if out := rig.exec(id, "grep ' /tmp ' /proc/mounts"); strings.Contains(out, "noexec") {
		t.Errorf("/tmp is mounted noexec: %q", out)
	}
}

// An image's own VOLUME is a capped tmpfs under a read-only root: the engine makes no anonymous
// volume for it, a write past the cap fails, and what the app writes never reaches the disk.
func TestRealEngineAnImageVolumeIsCappedAndNeverReachesTheDisk(t *testing.T) {
	rig := newRealRig(t)
	ctx := context.Background()
	// The project network exists once an app has been deployed.
	rig.deploy(itApp("one"), "dep-1")
	// Built here and never pulled, so it is no third-party image for the CI image list.
	repository := "excalibase-it-volume"
	tag := repository + ":" + shortHash(t.Name()+time.Now().String(), 6)
	build := exec.Command("docker", "build", "-q", "-t", tag, "-")
	build.Stdin = strings.NewReader("FROM " + itBusybox + "\nVOLUME /vol\n")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the image: %v\n%s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })

	app := itApp("two")
	app.ID, app.Name, app.Image, app.Disk = "app-vol", "vol", tag, nil
	app.Args = []string{"sleep", "600"}
	opts := RenderOptions("apps.example.com", "default")
	opts.DeployID, opts.EnvRevision = "dep-vol", "1"
	workload, err := k8s.RenderAppWorkload(rig.db, app, stubResolver{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := specFromWorkload(workload)
	if err != nil {
		t.Fatal(err)
	}
	id, err := rig.rt.createAppContainer(ctx, spec, 0)
	if err != nil {
		t.Fatalf("create through the proxy: %v", err)
	}
	t.Cleanup(func() {
		_ = rig.direct.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: true})
	})
	if err := rig.direct.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspect, err := rig.direct.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inspect.HostConfig.Tmpfs["/vol"], "size=") {
		t.Fatalf("the image's VOLUME /vol has no capped tmpfs: %v", inspect.HostConfig.Tmpfs)
	}
	for _, m := range inspect.Mounts {
		if m.Type == mount.TypeVolume && m.Destination == "/vol" {
			t.Fatalf("the engine made a volume for /vol: %+v", m)
		}
	}
	if out := rig.exec(id, "dd if=/dev/zero of=/vol/big bs=1M count=200"); !strings.Contains(out, "No space left") {
		t.Fatalf("200 MiB fit in the image volume: %q", out)
	}
}

// exec runs a shell line in the container and returns what it printed, stderr included.
func (r *realRig) exec(id, line string) string {
	r.t.Helper()
	ctx := context.Background()
	created, err := r.direct.ContainerExecCreate(ctx, id, container.ExecOptions{Cmd: []string{"sh", "-c", line + " 2>&1"}, AttachStdout: true, AttachStderr: true})
	if err != nil {
		r.t.Fatal(err)
	}
	attached, err := r.direct.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	defer attached.Close()
	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := attached.Reader.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return out.String()
}
