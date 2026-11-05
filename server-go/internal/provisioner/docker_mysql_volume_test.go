package provisioner

import (
	"context"
	"errors"
	"strings"
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

// EXC-682: v1.6 created the data volume labelled managed and nothing else, and
// the engine never adds a label to an existing volume. A minor upgrade after
// the update must still open it: its name encodes the project.
func TestDockerMySQLUpgradeOpensAVolumeV16LeftWithoutAnOwner(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDockerMySQL(t, engine)
	engine.volumeLabels[p1Volume] = map[string]string{excalibaseLabel: "true"}
	newer, _ := config.MySQLImage("8.4.9")
	if err := newTestDockerMySQL(engine).ReplaceImage(context.Background(), "p1", newer, testTier); err != nil {
		t.Fatalf("ReplaceImage over a v1.6 volume: %v", err)
	}
	if spec := engine.specs[result.Namespace]; spec.Image != newer || !engine.containers[result.Namespace].running {
		t.Fatalf("spec %+v", spec)
	}
	if owner := engine.volumeLabels[p1Volume][projectLabel]; owner != "" {
		t.Fatalf("the fake relabelled an existing volume (%q), which the engine never does", owner)
	}
}

// EXC-682: everything the replacement needs is checked while the old
// container still serves; a refusal leaves the database up and untouched.
func TestDockerMySQLUpgradeChecksTheReplacementBeforeRemovingTheContainer(t *testing.T) {
	newer, _ := config.MySQLImage("8.4.9")
	cases := map[string]struct {
		prepare func(engine *fakeEngine)
		want    string
	}{
		"a volume another project owns": {func(engine *fakeEngine) {
			engine.volumeLabels[p1Volume] = map[string]string{excalibaseLabel: "true", projectLabel: "p2"}
		}, "p2"},
		"a volume an app owns": {func(engine *fakeEngine) {
			engine.volumeLabels[p1Volume] = map[string]string{excalibaseLabel: "true", projectLabel: "p1", "excalibase.app": "a1"}
		}, "excalibase.app"},
		"a volume provisioning does not manage": {func(engine *fakeEngine) {
			engine.volumeLabels[p1Volume] = map[string]string{}
		}, "manage"},
		"a missing volume": {func(engine *fakeEngine) {
			delete(engine.volumes, p1Volume)
		}, "does not exist"},
		"an image that cannot be pulled": {func(engine *fakeEngine) {
			engine.failOn = "pull:" + newer
		}, "pull"},
	}
	for name, tc := range cases {
		engine := newFakeEngine()
		result := provisionDockerMySQL(t, engine)
		tc.prepare(engine)
		removesBefore := engine.indexOf("remove:")
		err := newTestDockerMySQL(engine).ReplaceImage(context.Background(), "p1", newer, testTier)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want it to name %q", name, err, tc.want)
		}
		if removesBefore != -1 || engine.indexOf("remove:") != -1 || engine.indexOf("stop:") != -1 {
			t.Errorf("%s: the old container was stopped or removed: %v", name, engine.calls)
		}
		if c := engine.containers[result.Namespace]; c == nil || !c.running {
			t.Errorf("%s: the database is not left running", name)
		}
	}
}

func TestRealClientReadsAVolumesLabels(t *testing.T) {
	created := map[string]map[string]string{"excalibase-vol-p1-mysql": {"excalibase.managed": "true"}}
	var removed []string
	c := volumeAPI(t, created, &removed)
	labels, err := c.VolumeLabels(context.Background(), "excalibase-vol-p1-mysql")
	if err != nil || labels["excalibase.managed"] != "true" || labels[projectLabel] != "" {
		t.Fatalf("labels %v err %v", labels, err)
	}
	if _, err := c.VolumeLabels(context.Background(), "gone"); !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("a missing volume: %v", err)
	}
}
