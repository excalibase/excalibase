//go:build integration

package dockerapps

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-682 on rootless Podman, through the real proxy: unsandboxed apps run in
// ranges of their own, an escape into another app's volume reads nothing, and
// a redeploy keeps the app's files. It needs a pool for three apps beyond the
// shared range, 65536 x 4 ids in /etc/subuid and /etc/subgid (init.sh --grow-subids
// --max-apps 3); with less it skips. Podman's `auto` userns also maps whatever a
// range leaves of a container's 65536 ids, so only whole ranges can share a pool.
//
// Run: DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock \
//      go test -tags=integration ./internal/dockerapps/ -run TestRealPodmanPerAppUserRanges -v
// IT_NO_CPU_LIMIT=1 where the user has no cpu cgroup controller.

func skipUnlessRootlessPodman(t *testing.T) {
	t.Helper()
	engine, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("no engine: %v", err)
	}
	defer engine.Close()
	info, err := engine.Info(context.Background())
	if err != nil {
		t.Skipf("no engine: %v", err)
	}
	if podman, _ := IsPodman(context.Background(), engine); !podman || !slicesContains(info.SecurityOptions, "name=rootless") {
		t.Skip("per-app user ranges are for rootless Podman")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman unshare is how the test reads file owners")
	}
}

func slicesContains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func perAppRig(t *testing.T) *realRig {
	t.Helper()
	skipUnlessRootlessPodman(t)
	rig := newRealRigWith(t, func(o *Options) { o.SandboxRuntime, o.Userns, o.Isolate = "", UsernsAuto, true })
	if !rig.rt.userns.perApp || rig.rt.userns.slots < 3 {
		t.Skipf("the platform user's pool holds %d app ranges, the test needs 3: %s", rig.rt.userns.slots, rig.rt.UsernsNotice())
	}
	return rig
}

// secretApp writes a file into its own disk with the default umask, as an ordinary app does, then serves.
func secretApp(id, secret string) *apphost.App {
	app := itApp(secret)
	app.ID, app.Name = id, id
	app.Args = []string{"sh", "-c", "echo " + secret + " > /data/secret && mkdir -p /www && echo ok > /www/index.html && exec httpd -f -p 8080 -h /www"}
	return app
}

// ownerInPlatformIDs is the id owning path as the platform user's namespace names it.
func ownerInPlatformIDs(t *testing.T, path string) uint32 {
	t.Helper()
	output, err := exec.Command("podman", "unshare", "stat", "-c", "%u", path).CombinedOutput()
	if err != nil {
		t.Fatalf("stat %s: %v\n%s", path, err, output)
	}
	owner, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 32)
	if err != nil {
		t.Fatalf("owner of %s: %q", path, output)
	}
	return uint32(owner)
}

func (r *realRig) mountpoint(app *apphost.App) string {
	r.t.Helper()
	vol, err := r.direct.VolumeInspect(context.Background(), r.rt.diskVolume(app.ID, 0))
	if err != nil {
		r.t.Fatal(err)
	}
	return vol.Mountpoint
}

func (r *realRig) assigned(appID string) usernsRange {
	r.t.Helper()
	got, err := r.rt.recordedUserns("proj-it", appID)
	if err != nil || got == nil {
		r.t.Fatalf("app %s has no range: %v", appID, err)
	}
	return *got
}

// readsAs runs `cat file` as root of the given range with the volume mounted, the way a
// process that escaped its container would meet another app's disk. It goes to the engine
// directly: the proxy exists to stop an app doing this, the range is what stops an escape.
func (r *realRig) readsAs(who usernsRange, volumeName string) error {
	return r.runsAs(who, volumeName, "cat", "/disk/secret")
}

// writesAs appends to the app's file as root of another range.
func (r *realRig) writesAs(who usernsRange, volumeName string) error {
	return r.runsAs(who, volumeName, "sh", "-c", "echo tampered >> /disk/secret")
}

func (r *realRig) runsAs(who usernsRange, volumeName string, command ...string) error {
	r.t.Helper()
	ctx := context.Background()
	created, err := r.direct.ContainerCreate(ctx, &container.Config{Image: itBusybox, Cmd: command},
		&container.HostConfig{UsernsMode: who.mode(), NetworkMode: "none",
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volumeName, Target: "/disk"}}}, nil, nil, "")
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = r.direct.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true}) }()
	if err := r.direct.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		r.t.Fatal(err)
	}
	waited, errs := r.direct.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case status := <-waited:
		if status.StatusCode != 0 {
			return &readError{code: status.StatusCode}
		}
		return nil
	case err := <-errs:
		r.t.Fatal(err)
	}
	return nil
}

type readError struct{ code int64 }

func (e *readError) Error() string { return "cat exited " + strconv.FormatInt(e.code, 10) }

func TestRealPodmanPerAppUserRanges(t *testing.T) {
	rig := perAppRig(t)
	ctx := context.Background()
	a, b := secretApp("app-ra", "alpha"), secretApp("app-rb", "bravo")
	rig.deploy(a, "dep-1")
	rig.deploy(b, "dep-1")

	rangeA, rangeB := rig.assigned(a.ID), rig.assigned(b.ID)
	if rangeA == rangeB || rangeA.overlaps(rangeB) {
		t.Fatalf("apps share ids: %+v and %+v", rangeA, rangeB)
	}
	// From the host: each app's file belongs to its own range, root of the app being its Base.
	if got := ownerInPlatformIDs(t, rig.mountpoint(a)+"/secret"); got != rangeA.Base {
		t.Fatalf("A's file belongs to id %d, want its range's root %d", got, rangeA.Base)
	}
	if got := ownerInPlatformIDs(t, rig.mountpoint(b)+"/secret"); got != rangeB.Base {
		t.Fatalf("B's file belongs to id %d, want %d", got, rangeB.Base)
	}

	// An escape from A, standing in A's range, cannot change B's file; B's own range can (so the
	// check can fail). Reading is not claimed: a file the app made with the default umask is world-readable
	// once the path is reachable, and on the host the volume sits under the platform user's 0700 storage.
	if err := rig.writesAs(rangeB, rig.rt.diskVolume(b.ID, 0)); err != nil {
		t.Fatalf("B cannot write its own disk: %v", err)
	}
	if err := rig.writesAs(rangeA, rig.rt.diskVolume(b.ID, 0)); err == nil {
		t.Fatal("A's range wrote to B's file")
	}
	if err := rig.readsAs(rangeA, rig.rt.diskVolume(a.ID, 0)); err != nil {
		t.Fatalf("A cannot read its own disk: %v", err)
	}

	// A redeploy (new containers) stays in the range and keeps the files.
	rig.deploy(a, "dep-2")
	if again := rig.assigned(a.ID); again != rangeA {
		t.Fatalf("a redeploy moved A from %+v to %+v", rangeA, again)
	}
	if got := ownerInPlatformIDs(t, rig.mountpoint(a)+"/secret"); got != rangeA.Base {
		t.Fatalf("after the redeploy A's file belongs to %d, want %d", got, rangeA.Base)
	}
	if err := rig.readsAs(rangeA, rig.rt.diskVolume(a.ID, 0)); err != nil {
		t.Fatalf("A lost its file after the redeploy: %v", err)
	}
	list, err := rig.direct.ContainerList(ctx, container.ListOptions{Filters: labelFilter(labelApp, a.ID)})
	if err != nil || len(list) != 1 {
		t.Fatalf("A's containers: %d (%v)", len(list), err)
	}
	if output, err := exec.Command("docker", "exec", list[0].ID, "cat", "/data/secret").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "alpha" {
		t.Fatalf("the redeployed app reads %q (%v)", output, err)
	}
	if _, err := rig.rt.AppDiskUsage(ctx, rig.db, a.ID, *a.Disk, k8s.DiskJobOptions{Timeout: time.Minute}); err != nil {
		t.Fatalf("disk usage in A's range: %v", err)
	}

	// Pause and resume, as a restart of the app: same range, same files.
	if err := rig.rt.PauseAppWorkload(ctx, rig.db, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := rig.rt.ResumeAppWorkload(ctx, rig.db, a.ID, a.Name, domain.Free, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := rig.readsAs(rangeA, rig.rt.diskVolume(a.ID, 0)); err != nil {
		t.Fatalf("A lost its file across a pause: %v", err)
	}

	// Deleting A frees its range, and its disk with it: the next app takes it.
	if err := rig.rt.DeleteAppWorkload(ctx, rig.db, a.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	c := secretApp("app-rc", "charlie")
	rig.deploy(c, "dep-1")
	if got := rig.assigned(c.ID); got != rangeA {
		t.Fatalf("A's freed range %+v was not the next app's: %+v", rangeA, got)
	}
	if err := rig.writesAs(rangeB, rig.rt.diskVolume(c.ID, 0)); err == nil {
		t.Fatal("B wrote to C's file")
	}
	for _, id := range []string{b.ID, c.ID} {
		if err := rig.rt.DeleteAppWorkload(ctx, rig.db, id, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
}
