package provisioner

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

const p1Volume = "excalibase-vol-p1-mysql"

// EXC-592: the data lives on the project's own named volume, so a new image
// can open it (a minor upgrade) and only a deletion or a restore that
// replaces the data removes it.
func TestDockerMySQLKeepsItsDataOnTheProjectsVolume(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDockerMySQL(t, engine)
	spec := engine.specs[result.Namespace]
	if spec.DataVolumeName != p1Volume || spec.DataVolume != MySQLDataDir || !engine.volumes[p1Volume] {
		t.Fatalf("data volume %q at %q, volumes %v", spec.DataVolumeName, spec.DataVolume, engine.volumes)
	}
	mysql := newTestDockerMySQL(engine)
	if err := mysql.RemoveContainer(context.Background(), result.Namespace); err != nil || !engine.volumes[p1Volume] {
		t.Fatalf("removing the container took the data: %v", err)
	}
	if err := mysql.Deprovision(context.Background(), result.Namespace, "p1"); err != nil || engine.volumes[p1Volume] {
		t.Fatalf("a deleted project kept its data: %v %v", err, engine.volumes)
	}
}

func TestDockerMySQLRemovingTheDataLeavesAnEmptyStart(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDockerMySQL(t, engine)
	if err := newTestDockerMySQL(engine).RemoveData(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if _, exists := engine.containers[result.Namespace]; exists || engine.volumes[p1Volume] {
		t.Fatalf("container or data left: %v", engine.volumes)
	}
}

func TestDockerMySQLMinorUpgradeOpensTheSameDataOnTheNewImage(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDockerMySQL(t, engine)
	older, _ := config.MySQLImage("8.4.9")
	if err := newTestDockerMySQL(engine).ReplaceImage(context.Background(), "p1", older, testTier); err != nil {
		t.Fatalf("ReplaceImage: %v", err)
	}
	spec := engine.specs[result.Namespace]
	if spec.Image != older || spec.DataVolumeName != p1Volume || !engine.containers[result.Namespace].running {
		t.Fatalf("spec %+v", spec)
	}
	for _, call := range engine.calls {
		if call == "rmvolume:"+p1Volume {
			t.Fatal("the upgrade removed the data")
		}
	}
}

// A container that cannot be removed stops a rebuild before the data is
// touched.
func TestDockerMySQLRebuildStopsWhenTheOldContainerStays(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDockerMySQL(t, engine)
	engine.failOn = "remove:" + result.Namespace
	mysql := newTestDockerMySQL(engine)
	image, _ := config.MySQLImage(config.DefaultMySQLVersion())
	if err := mysql.ReplaceImage(context.Background(), "p1", image, testTier); err == nil {
		t.Fatal("replaced the image of a container that stayed")
	}
	if err := mysql.RemoveData(context.Background(), "p1"); err == nil || !engine.volumes[p1Volume] {
		t.Fatalf("data removed under a container that stayed: %v", err)
	}
}
