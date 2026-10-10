package dockerapps

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func diskApp() *apphost.App {
	app := freeApp()
	app.Disk = &apphost.AppDisk{MountPath: "/data", Size: "1Gi"}
	return app
}

var diskJobs = k8s.DiskJobOptions{Image: "tools:1", RuntimeClass: "runsc", Timeout: time.Second}

func TestCreateAppDiskMakesAVolumeOpenToAnyUser(t *testing.T) {
	h := newHarness(t, nil)
	var ran [][]string
	h.engine.toolOutput = func(cmd []string) (string, int) { ran = append(ran, cmd); return "", 0 }
	if err := h.rt.CreateAppDisk(context.Background(), testDB, diskApp(), "", diskJobs); err != nil {
		t.Fatal(err)
	}
	labels, ok := h.engine.volumes["excalibase-vol-app-disk-app-01"]
	if !ok || labels[labelApp] != "app-01" || labels[labelProject] != testProject || labels[labelGeneration] != "0" {
		t.Fatalf("volumes %v", h.engine.volumes)
	}
	if len(ran) != 1 || !slices.Contains(ran[0], "0777") {
		t.Fatalf("tool commands %v", ran)
	}
	for _, c := range h.engine.containers {
		if c.config.Labels[labelComponent] == componentDiskTool {
			t.Fatalf("tool container %s left behind", c.name)
		}
	}
	if err := h.rt.CreateAppDisk(context.Background(), testDB, diskApp(), "", diskJobs); err != nil || len(ran) != 1 {
		t.Fatalf("a second create touched the disk again: %v (%d runs)", err, len(ran))
	}
}

func TestToolContainersRunWithoutANetworkInTheSandbox(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.toolOutput = func(cmd []string) (string, int) {
		for _, c := range h.engine.containers {
			if c.config.Labels[labelComponent] == componentDiskTool {
				if c.host.NetworkMode != "none" || c.host.Runtime != "runsc" || len(c.host.Mounts) != 1 {
					t.Errorf("tool container %+v", c.host)
				}
			}
		}
		return "", 0
	}
	if err := h.rt.CreateAppDisk(context.Background(), testDB, diskApp(), "", diskJobs); err != nil {
		t.Fatal(err)
	}
}

func TestAppDiskUsageReadsWhatTheDiskHolds(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	disk := *diskApp().Disk
	if _, err := h.rt.AppDiskUsage(ctx, testDB, "app-01", disk, diskJobs); !errors.Is(err, k8s.ErrAppDiskNotCreated) {
		t.Fatalf("before creation: %v", err)
	}
	h.engine.toolOutput = func(cmd []string) (string, int) {
		if slices.Contains(cmd, "du") {
			return "2048\t/disk\n", 0
		}
		return "", 0
	}
	if err := h.rt.CreateAppDisk(ctx, testDB, diskApp(), "", diskJobs); err != nil {
		t.Fatal(err)
	}
	usage, err := h.rt.AppDiskUsage(ctx, testDB, "app-01", disk, diskJobs)
	if err != nil || usage.UsedBytes != 2048*1024 || usage.SizeBytes != 1<<30 {
		t.Fatalf("usage %+v (%v)", usage, err)
	}
	h.engine.toolOutput = func([]string) (string, int) { return "du: cannot read", 1 }
	if _, err := h.rt.AppDiskUsage(ctx, testDB, "app-01", disk, diskJobs); !errors.Is(err, k8s.ErrAppDiskJob) {
		t.Fatalf("failed probe: %v", err)
	}
}

func TestGrowAppDiskOnlyNeedsTheVolume(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	if err := h.rt.GrowAppDisk(ctx, testDB, "app-01", 0, "2Gi"); !errors.Is(err, k8s.ErrAppDiskNotCreated) {
		t.Fatalf("grow before creation: %v", err)
	}
	if err := h.rt.CreateAppDisk(ctx, testDB, diskApp(), "", diskJobs); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.GrowAppDisk(ctx, testDB, "app-01", 0, "2Gi"); err != nil {
		t.Fatal(err)
	}
}

func TestLoweringCopiesOntoANewGenerationAndRepoints(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	app := diskApp()
	if err := h.rt.CreateAppDisk(ctx, testDB, app, "", diskJobs); err != nil {
		t.Fatal(err)
	}
	h.rollout(app, "dep-1")
	var copied []string
	h.engine.toolOutput = func(cmd []string) (string, int) { copied = append(copied, strings.Join(cmd, " ")); return "", 0 }
	to := apphost.AppDisk{MountPath: "/data", Size: "512Mi", Generation: 1}
	if err := h.rt.CopyAppDisk(ctx, testDB, app, to, "", diskJobs); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.engine.volumes["excalibase-vol-app-disk-app-01-g1"]; !ok || len(copied) != 1 || !strings.Contains(copied[0], "cp -a /from/. /to/") {
		t.Fatalf("copy: volumes %v commands %v", h.engine.volumes, copied)
	}
	claim := k8s.AppDiskClaimName("app-01", 1)
	if err := h.rt.RepointAppDisk(ctx, testDB, "app-01", claim); err == nil {
		t.Fatal("repointed a running app")
	}
	if err := h.rt.PauseAppWorkload(ctx, testDB, "app-01"); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.RepointAppDisk(ctx, testDB, "app-01", claim); err != nil {
		t.Fatal(err)
	}
	c := h.engine.appContainers("app-01")[0]
	if c.host.Mounts[0].Source != "excalibase-vol-"+claim || c.config.Labels[labelDisk] != claim {
		t.Fatalf("mount %+v label %q", c.host.Mounts, c.config.Labels[labelDisk])
	}
	if err := h.rt.DeleteOtherAppDisks(ctx, testDB, "app-01", 1, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, old := h.engine.volumes["excalibase-vol-app-disk-app-01"]; old || len(h.engine.volumes) != 1 {
		t.Fatalf("volumes after the move %v", h.engine.volumes)
	}
}

// Another install's volumes may carry the same labels; only this one's prefix is ours.
func TestDiskRemovalKeepsToItsOwnPrefix(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.volumes["elsewhere-app-disk-app-01"] = map[string]string{labelManaged: "true", labelApp: "app-01", labelProject: testProject}
	if err := h.rt.DeleteOtherAppDisks(context.Background(), testDB, "app-01", 0, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.TeardownProject(context.Background(), testProject, testDB); err != nil {
		t.Fatal(err)
	}
	if _, kept := h.engine.volumes["elsewhere-app-disk-app-01"]; !kept {
		t.Fatal("removed a volume outside the volume prefix")
	}
}
