package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// quotaFixture: images 1..5 of the app, oldest first, 100 bytes of layer each
// plus a 10-byte manifest; the app runs image 5. Another project's image sits beside.
func quotaFixture(t *testing.T) (*RegistrySweeper, *fakeRegistryAdmin) {
	t.Helper()
	app := sampleDeployApp()
	repo := app.ProjectID + "/" + app.Name
	app.Image = appregistrytestHost + "/" + repo + "@" + sweepDigest(5)
	deploys := newFakeDeployStore()
	deploys.deploys = []*apphost.Deploy{{ID: "d5", ProjectID: app.ProjectID, AppID: app.ID, Image: app.Image, Status: apphost.DeployStatusSucceeded}}
	registry := &fakeRegistryAdmin{tags: map[string][]string{"proj-other/api": {pushedTag(t, sweepDigest(90), 99*time.Hour)}}, blobs: map[string]map[string]int64{}}
	for n := 1; n <= 5; n++ {
		tag := pushedTag(t, sweepDigest(n), time.Duration(10-n)*time.Hour)
		registry.tags[repo] = append(registry.tags[repo], tag)
		registry.blobs[tag] = map[string]int64{sweepDigest(n): 10, sweepDigest(100 + n): 100}
	}
	registry.blobs[registry.tags["proj-other/api"][0]] = map[string]int64{sweepDigest(90): 5000}
	sweeper := NewRegistrySweeper(registry, newFakeAppStoreForDeploy(app), deploys, func(context.Context, string) (bool, error) { return true, nil })
	sweeper.now = func() time.Time { return sweepNow }
	return sweeper, registry
}

func TestMakeRoom_TakesTheOldestImagesNoAppRunsUntilItFits(t *testing.T) {
	sweeper, registry := quotaFixture(t)
	incoming := map[string]int64{sweepDigest(6): 10, sweepDigest(106): 100}
	if err := sweeper.MakeRoom(context.Background(), sampleDeployApp().ProjectID, incoming, 330); err != nil {
		t.Fatal(err)
	}
	want := []string{sweepDigest(1), sweepDigest(2), sweepDigest(3)}
	got := []string{}
	for _, entry := range registry.deleted {
		got = append(got, entry[len(entry)-len(sweepDigest(0)):])
	}
	if !slices.Equal(got, want) {
		t.Fatalf("untagged %v, want the three oldest", got)
	}
}

func TestMakeRoom_CountsALayerTheImagesShareOnce(t *testing.T) {
	sweeper, registry := quotaFixture(t)
	// 550 in use; the new image's only new bytes are its 10-byte manifest.
	incoming := map[string]int64{sweepDigest(7): 10, sweepDigest(105): 100}
	if err := sweeper.MakeRoom(context.Background(), sampleDeployApp().ProjectID, incoming, 560); err != nil || len(registry.deleted) != 0 {
		t.Fatalf("%v, untagged %v", err, registry.deleted)
	}
}

func TestMakeRoom_NeverTakesWhatRuns(t *testing.T) {
	sweeper, registry := quotaFixture(t)
	incoming := map[string]int64{sweepDigest(8): 500}
	if err := sweeper.MakeRoom(context.Background(), sampleDeployApp().ProjectID, incoming, 400); !errors.Is(err, ErrRegistryFull) {
		t.Fatalf("%v", err)
	}
	for _, entry := range registry.deleted {
		if entry[len(entry)-len(sweepDigest(0)):] == sweepDigest(5) || entry[:len("proj-other")] == "proj-other" {
			t.Errorf("untagged %s", entry)
		}
	}
}

func TestMakeRoom_FailsWhenTheRegistryCannotAnswer(t *testing.T) {
	project := sampleDeployApp().ProjectID
	incoming := map[string]int64{sweepDigest(9): 10_000}
	for name, breakIt := range map[string]func(*fakeRegistryAdmin){
		"catalog": func(f *fakeRegistryAdmin) { f.listErr = errors.New("down") },
		"tags":    func(f *fakeRegistryAdmin) { f.tagsErr = errors.New("down") },
		"blobs":   func(f *fakeRegistryAdmin) { f.blobsErr = errors.New("down") },
		"untag":   func(f *fakeRegistryAdmin) { f.deleteErr = errors.New("down") },
	} {
		sweeper, registry := quotaFixture(t)
		breakIt(registry)
		if err := sweeper.MakeRoom(context.Background(), project, incoming, 20_000); name != "untag" && err == nil {
			t.Errorf("%s: room made", name)
		}
		if err := sweeper.MakeRoom(context.Background(), project, incoming, 10); err == nil {
			t.Errorf("%s: room made over the quota", name)
		}
	}
	sweeper, _ := quotaFixture(t)
	sweeper.live = func(context.Context, string) (bool, error) { return false, errors.New("db down") }
	if err := sweeper.MakeRoom(context.Background(), project, incoming, 20_000); err == nil {
		t.Error("the running images unknown")
	}
}
