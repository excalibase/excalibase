//go:build integration

package dockerapps

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/errdefs"
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
	var probeVolume string
	for _, m := range inspect.Mounts {
		if m.Destination == "/"+probeDir {
			probeVolume = m.Name
		}
	}
	if probeVolume == "" {
		t.Fatalf("no probe volume among %+v", inspect.Mounts)
	}
	if out := rig.exec(id, "touch /rootfs-probe"); !strings.Contains(out, "Read-only") {
		t.Fatalf("a write to the root answered %q, want a read-only error", out)
	}
	if out := rig.exec(id, "touch /tmp/ok && touch /data/ok && echo wrote"); !strings.Contains(out, "wrote") {
		t.Fatalf("writes to /tmp and the disk answered %q", out)
	}

	if err := rig.rt.DeleteAppWorkload(ctx, rig.db, "app-it", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.direct.VolumeInspect(ctx, probeVolume); !errdefs.IsNotFound(err) {
		t.Fatalf("the probe volume %s outlived its container: %v", probeVolume, err)
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
