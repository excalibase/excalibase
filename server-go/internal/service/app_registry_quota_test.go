package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// quotaFixture: images 1..5 of the app, oldest first and all past the push
// grace, 100 bytes of layer each plus a 10-byte manifest; the app runs image 5.
// Another project's image sits beside.
func quotaFixture(t *testing.T) (*RegistrySweeper, *fakeRegistryAdmin, *fakeDeployStore) {
	t.Helper()
	app := sampleDeployApp()
	app.Image = appregistrytestHost + "/" + quotaRepo() + "@" + sweepDigest(5)
	deploys := newFakeDeployStore()
	deploys.deploys = []*apphost.Deploy{{ID: "d5", ProjectID: app.ProjectID, AppID: app.ID, Image: app.Image, Status: apphost.DeployStatusSucceeded}}
	registry := &fakeRegistryAdmin{tags: map[string][]string{"proj-other/api": {pushedTag(t, sweepDigest(90), 99*time.Hour)}}, blobs: map[string]map[string]int64{}}
	for n := 1; n <= 5; n++ {
		addPush(t, registry, n, RegistryPushGrace+time.Duration(10-n)*time.Hour, 100)
	}
	registry.blobs[registry.tags["proj-other/api"][0]] = map[string]int64{sweepDigest(90): 5000}
	sweeper := NewRegistrySweeper(registry, newFakeAppStoreForDeploy(app), deploys, func(context.Context, string) (bool, error) { return true, nil })
	sweeper.now = func() time.Time { return sweepNow }
	return sweeper, registry, deploys
}

func quotaRepo() string {
	app := sampleDeployApp()
	return app.ProjectID + "/" + app.Name
}

// addPush tags image n, pushed age ago: a 10-byte manifest and a layer of the given size.
func addPush(t *testing.T, registry *fakeRegistryAdmin, n int, age time.Duration, layer int64) {
	t.Helper()
	tag := pushedTag(t, sweepDigest(n), age)
	registry.tags[quotaRepo()] = append(registry.tags[quotaRepo()], tag)
	registry.blobs[tag] = map[string]int64{sweepDigest(n): 10, sweepDigest(100 + n): layer}
}

// untaggedImages is the image numbers that lost a tag, in order.
func untaggedImages(registry *fakeRegistryAdmin) []int {
	got := []int{}
	for _, entry := range registry.deleted {
		for n := 0; n < 200; n++ {
			if entry == quotaRepo()+"@"+sweepDigest(n) {
				got = append(got, n)
			}
		}
	}
	return got
}

func TestMakeRoom_TakesTheOldestImagesNoAppRunsUntilItFits(t *testing.T) {
	sweeper, registry, _ := quotaFixture(t)
	addPush(t, registry, 6, time.Minute, 100)
	if err := sweeper.MakeRoom(context.Background(), sampleDeployApp().ProjectID, sweepDigest(6), 330); err != nil {
		t.Fatal(err)
	}
	if got := untaggedImages(registry); !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("untagged %v, want the three oldest", got)
	}
}

func TestFits_CountsALayerTheImagesShareOnce(t *testing.T) {
	sweeper, registry, _ := quotaFixture(t)
	// Only image 5 is kept: 110 bytes; the new image's only new bytes are its 10-byte manifest.
	incoming := map[string]int64{sweepDigest(7): 10, sweepDigest(105): 100}
	if err := sweeper.Fits(context.Background(), sampleDeployApp().ProjectID, incoming, 120); err != nil || len(registry.deleted) != 0 {
		t.Fatalf("%v, untagged %v", err, registry.deleted)
	}
}

func TestMakeRoom_NeverTakesWhatRuns(t *testing.T) {
	sweeper, registry, _ := quotaFixture(t)
	project := sampleDeployApp().ProjectID
	err := sweeper.Fits(context.Background(), project, map[string]int64{sweepDigest(8): 500}, 400)
	var full *RegistryFullError
	if !errors.Is(err, ErrRegistryFull) || !errors.As(err, &full) || full.Used != 110 || full.Incoming != 500 || full.Quota != 400 {
		t.Fatalf("%v", err)
	}
	if len(registry.deleted) != 0 {
		t.Fatalf("a build that cannot fit untagged %v", registry.deleted)
	}
	if err := sweeper.MakeRoom(context.Background(), project, sweepDigest(5), 10); !errors.Is(err, ErrRegistryFull) {
		t.Fatalf("%v", err)
	}
	if got := untaggedImages(registry); !slices.Equal(got, []int{1, 2, 3, 4}) || len(registry.deleted) != 4 {
		t.Fatalf("untagged %v", registry.deleted)
	}
}

// The quota spares what the sweep spares inside the grace: the app's
// RegistryRecentPushes newest pushes no deploy names yet.
func TestMakeRoom_SparesTheNewestPushesInsideTheGrace(t *testing.T) {
	sweeper, registry, _ := quotaFixture(t)
	for n := 6; n <= 10; n++ {
		addPush(t, registry, n, time.Duration(11-n)*time.Hour, 100)
	}
	if err := sweeper.MakeRoom(context.Background(), sampleDeployApp().ProjectID, sweepDigest(10), 10); !errors.Is(err, ErrRegistryFull) {
		t.Fatalf("%v", err)
	}
	if got := untaggedImages(registry); !slices.Equal(got, []int{1, 2, 3, 4, 6}) {
		t.Fatalf("untagged %v, want 7, 8 and 9 kept inside the grace", got)
	}
}

func TestMakeRoom_SparesTheImagesOfDeploysInFlight(t *testing.T) {
	sweeper, registry, deploys := quotaFixture(t)
	app := sampleDeployApp()
	for i, status := range []string{apphost.DeployStatusPending, apphost.DeployStatusRolling} {
		deploys.deploys = append(deploys.deploys, &apphost.Deploy{ID: "f" + status, ProjectID: app.ProjectID, AppID: app.ID,
			Image: appregistrytestHost + "/" + quotaRepo() + "@" + sweepDigest(2+i), Status: status})
	}
	if err := sweeper.MakeRoom(context.Background(), app.ProjectID, sweepDigest(5), 10); !errors.Is(err, ErrRegistryFull) {
		t.Fatalf("%v", err)
	}
	if got := untaggedImages(registry); !slices.Equal(got, []int{1, 4}) {
		t.Fatalf("untagged %v, want the pending and rolling deploys' images kept", got)
	}
}

func TestMakeRoom_FailsWhenTheRegistryCannotAnswer(t *testing.T) {
	project := sampleDeployApp().ProjectID
	incoming := map[string]int64{sweepDigest(9): 10_000}
	for name, breakIt := range map[string]func(*fakeRegistryAdmin){
		"catalog": func(f *fakeRegistryAdmin) { f.listErr = errors.New("down") },
		"tags":    func(f *fakeRegistryAdmin) { f.tagsErr = errors.New("down") },
		"blobs":   func(f *fakeRegistryAdmin) { f.blobsErr = errors.New("down") },
	} {
		sweeper, registry, _ := quotaFixture(t)
		breakIt(registry)
		if err := sweeper.Fits(context.Background(), project, incoming, 20_000); err == nil {
			t.Errorf("%s: room found", name)
		}
		if err := sweeper.MakeRoom(context.Background(), project, sweepDigest(5), 10); err == nil || len(registry.deleted) != 0 {
			t.Errorf("%s: room made: %v", name, registry.deleted)
		}
	}
	sweeper, registry, _ := quotaFixture(t)
	registry.deleteErr = errors.New("down")
	if err := sweeper.MakeRoom(context.Background(), project, sweepDigest(5), 20_000); err != nil {
		t.Errorf("within the quota nothing is untagged, so nothing fails: %v", err)
	}
	if err := sweeper.MakeRoom(context.Background(), project, sweepDigest(5), 10); err == nil {
		t.Error("an untag that failed made room")
	}
	sweeper, _, _ = quotaFixture(t)
	sweeper.live = func(context.Context, string) (bool, error) { return false, errors.New("db down") }
	if err := sweeper.Fits(context.Background(), project, incoming, 20_000); err == nil {
		t.Error("the running images unknown")
	}
}

// recordBuilds adds images first..last pushed inside the grace, an hour apart
// with last the newest, each deployed and succeeded; the app runs last.
func recordBuilds(t *testing.T, registry *fakeRegistryAdmin, deploys *fakeDeployStore, apps *fakeAppStoreForDeploy, first, last int) {
	t.Helper()
	app := sampleDeployApp()
	for n := first; n <= last; n++ {
		addPush(t, registry, n, time.Duration(last-n)*time.Hour+time.Minute, 100)
		image := appregistrytestHost + "/" + quotaRepo() + "@" + sweepDigest(n)
		deploys.deploys = append(deploys.deploys, &apphost.Deploy{ID: fmt.Sprintf("d%d", n), ProjectID: app.ProjectID, AppID: app.ID,
			Image: image, Status: apphost.DeployStatusSucceeded})
		apps.apps[app.ProjectID+"/"+app.ID].Image = image
	}
}

// A push inside the grace is spared only until a deploy records it: builds 7
// to 10 deployed within the day leave room for build 11 beside the one running.
func TestFits_APushWhoseDeployIsRecordedIsNotSparedByTheGrace(t *testing.T) {
	sweeper, registry, deploys := quotaFixture(t)
	recordBuilds(t, registry, deploys, sweeper.apps.(*fakeAppStoreForDeploy), 7, 10)
	project := sampleDeployApp().ProjectID
	incoming := map[string]int64{sweepDigest(11): 10, sweepDigest(111): 100}
	if err := sweeper.Fits(context.Background(), project, incoming, 330); err != nil {
		t.Fatalf("build 11: %v", err)
	}
	addPush(t, registry, 11, 0, 100)
	if err := sweeper.MakeRoom(context.Background(), project, sweepDigest(11), 330); err != nil {
		t.Fatal(err)
	}
	if got := untaggedImages(registry); !slices.Equal(got, []int{1, 2, 3, 4, 5, 7, 8}) {
		t.Fatalf("untagged %v, want everything but 9, 10 (running) and 11 (pushed)", got)
	}
}

// The quota keeps what runs, not the sweep's last RegistryKeepDeploys deploys:
// older deployed images give way to a new build.
func TestFits_DoesNotKeepTheSweepsRecentDeploys(t *testing.T) {
	sweeper, registry, deploys := quotaFixture(t)
	app := sampleDeployApp()
	for n := 1; n <= 4; n++ {
		deploys.deploys = append([]*apphost.Deploy{{ID: fmt.Sprintf("old%d", n), ProjectID: app.ProjectID, AppID: app.ID,
			Image: appregistrytestHost + "/" + quotaRepo() + "@" + sweepDigest(n), Status: apphost.DeployStatusSucceeded}}, deploys.deploys...)
	}
	if err := sweeper.Fits(context.Background(), app.ProjectID, map[string]int64{sweepDigest(12): 110}, 220); err != nil {
		t.Fatalf("%v", err)
	}
	if err := sweeper.MakeRoom(context.Background(), app.ProjectID, sweepDigest(5), 110); err != nil {
		t.Fatal(err)
	}
	if got := untaggedImages(registry); !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Fatalf("untagged %v", got)
	}
}
