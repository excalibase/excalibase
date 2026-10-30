package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
)

type fakeRegistryAdmin struct {
	onTags           func()
	untagged         []string
	blobs            map[string]map[string]int64
	blobsErr         error
	tags             map[string][]string
	deleted          []string
	listErr, tagsErr error
	deleteErr        error
}

func (f *fakeRegistryAdmin) Repositories(context.Context) ([]string, error) {
	repos := make([]string, 0, len(f.tags))
	for repo := range f.tags {
		repos = append(repos, repo)
	}
	slices.Sort(repos)
	return repos, f.listErr
}

func (f *fakeRegistryAdmin) Tags(_ context.Context, repository string) ([]string, error) {
	if f.onTags != nil {
		f.onTags()
		f.onTags = nil
	}
	return f.tags[repository], f.tagsErr
}

func (f *fakeRegistryAdmin) Blobs(_ context.Context, _, reference string) (map[string]int64, error) {
	if f.blobsErr != nil {
		return nil, f.blobsErr
	}
	return f.blobs[reference], nil
}

// Untag records the image a tag named, so tests read what lost its tag.
func (f *fakeRegistryAdmin) Untag(_ context.Context, repository, tag string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	digest, _, _ := appregistry.ParsePushTag(tag)
	f.deleted = append(f.deleted, repository+"@"+digest)
	f.untagged = append(f.untagged, tag)
	return nil
}

func sweepDigest(n int) string { return fmt.Sprintf("sha256:%064x", n) }

var sweepNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func pushedTag(t *testing.T, digest string, age time.Duration) string {
	t.Helper()
	tag, err := appregistry.PushTag(digest, sweepNow.Add(-age))
	if err != nil {
		t.Fatal(err)
	}
	return tag
}

// sweepFixture is one live app with 12 git-build deploys (digests 1..12, the
// app now on 12) and a deleted app's repository beside it.
func sweepFixture(t *testing.T) (*RegistrySweeper, *fakeRegistryAdmin, *fakeDeployStore) {
	t.Helper()
	app := sampleDeployApp()
	app.Image = appregistrytestHost + "/" + app.ProjectID + "/" + app.Name + "@" + sweepDigest(12)
	apps := newFakeAppStoreForDeploy(app)
	deploys := newFakeDeployStore()
	repo := app.ProjectID + "/" + app.Name
	registry := &fakeRegistryAdmin{tags: map[string][]string{repo: {"latest"}, app.ProjectID + "/gone": {pushedTag(t, sweepDigest(50), 48*time.Hour)}}}
	for n := 1; n <= 12; n++ {
		deploys.deploys = append(deploys.deploys, &apphost.Deploy{ID: fmt.Sprint(n), ProjectID: app.ProjectID, AppID: app.ID,
			Image: appregistrytestHost + "/" + repo + "@" + sweepDigest(n), Source: apphost.DeploySourceGitBuild, Status: apphost.DeployStatusSucceeded})
		registry.tags[repo] = append(registry.tags[repo], pushedTag(t, sweepDigest(n), 72*time.Hour))
	}
	exists := func(_ context.Context, projectID string) (bool, error) { return projectID == app.ProjectID, nil }
	sweeper := NewRegistrySweeper(registry, apps, deploys, exists)
	sweeper.now = func() time.Time { return sweepNow }
	return sweeper, registry, deploys
}

const appregistrytestHost = "registry.excalibase.internal"

func TestRegistrySweep_KeepsTheLastTenDeploysAndDeletesAGoneAppsImages(t *testing.T) {
	sweeper, registry, _ := sweepFixture(t)
	app := sampleDeployApp()
	repo := app.ProjectID + "/" + app.Name
	report, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{repo + "@" + sweepDigest(1), repo + "@" + sweepDigest(2), app.ProjectID + "/gone@" + sweepDigest(50)}
	slices.Sort(registry.deleted)
	slices.Sort(want)
	if !slices.Equal(registry.deleted, want) || report.Deleted != 3 || report.Failed != 0 || report.Repositories != 2 {
		t.Fatalf("deleted %v, report %+v", registry.deleted, report)
	}
}

func TestRegistrySweep_SparesARecentPushAndTheImageTheAppNames(t *testing.T) {
	sweeper, registry, deploys := sweepFixture(t)
	app := sampleDeployApp()
	repo := app.ProjectID + "/" + app.Name
	// Digest 1 was pushed again an hour ago (its deploy not yet recorded); the
	// app was rolled back to digest 2, outside the last ten deploys.
	registry.tags[repo] = append(registry.tags[repo], pushedTag(t, sweepDigest(1), time.Hour), pushedTag(t, sweepDigest(99), time.Hour))
	for _, d := range deploys.deploys {
		d.Image = appregistrytestHost + "/" + repo + "@" + sweepDigest(100)
	}
	deploys.deploys[11].Digest = sweepDigest(12)
	apps := newFakeAppStoreForDeploy(app)
	apps.apps[app.ProjectID+"/"+app.ID].Image = appregistrytestHost + "/" + repo + "@" + sweepDigest(2)
	sweeper.apps = apps
	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []int{2, 12, 99} {
		if slices.Contains(registry.deleted, repo+"@"+sweepDigest(kept)) {
			t.Errorf("digest %d deleted", kept)
		}
	}
	// Digest 1 loses its old tag only: the new one keeps the image.
	if fresh := pushedTag(t, sweepDigest(1), time.Hour); slices.Contains(registry.untagged, fresh) || !slices.Contains(registry.untagged, pushedTag(t, sweepDigest(1), 72*time.Hour)) {
		t.Errorf("untagged %v", registry.untagged)
	}
	if !slices.Contains(registry.deleted, repo+"@"+sweepDigest(3)) {
		t.Errorf("an image nothing names stays: %v", registry.deleted)
	}
}

func TestRegistrySweep_ADeletedProjectKeepsNothing(t *testing.T) {
	sweeper, registry, _ := sweepFixture(t)
	sweeper.live = func(context.Context, string) (bool, error) { return false, nil }
	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(registry.deleted) != 13 {
		t.Errorf("deleted %d of 13", len(registry.deleted))
	}
}

// The image the cluster runs is the last deploy that succeeded, however many failed after it.
func TestRegistrySweep_KeepsTheLastSuccessBehindAnyNumberOfFailures(t *testing.T) {
	sweeper, registry, deploys := sweepFixture(t)
	app := sampleDeployApp()
	repo := app.ProjectID + "/" + app.Name
	for i, d := range deploys.deploys {
		if i > 0 {
			d.Status = apphost.DeployStatusFailed
		}
	}
	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(registry.deleted, repo+"@"+sweepDigest(1)) || !slices.Contains(registry.deleted, repo+"@"+sweepDigest(2)) {
		t.Errorf("deleted %v: digest 1 ran last, digest 2 never did", registry.deleted)
	}
}

// Pushes inside the grace that no deploy names are capped, so one app cannot fill the registry in a day.
func TestRegistrySweep_CapsRecentPushesNoDeployNames(t *testing.T) {
	sweeper, registry, _ := sweepFixture(t)
	app := sampleDeployApp()
	repo := app.ProjectID + "/" + app.Name
	for n := 60; n < 66; n++ {
		registry.tags[repo] = append(registry.tags[repo], pushedTag(t, sweepDigest(n), time.Duration(n)*time.Minute))
	}
	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	for n := 60; n < 66; n++ {
		if got := slices.Contains(registry.deleted, repo+"@"+sweepDigest(n)); got != (n >= 60+RegistryRecentPushes) {
			t.Errorf("recent push %d deleted: %v", n, got)
		}
	}
}

// A deploy recorded while the sweep listed the tags keeps its image.
func TestRegistrySweep_ReadsTheKeepSetAgainBeforeRemoving(t *testing.T) {
	sweeper, registry, deploys := sweepFixture(t)
	app := sampleDeployApp()
	repo := app.ProjectID + "/" + app.Name
	registry.onTags = func() {
		deploys.deploys = append(deploys.deploys, &apphost.Deploy{ID: "rollback", ProjectID: app.ProjectID, AppID: app.ID,
			Image: appregistrytestHost + "/" + repo + "@" + sweepDigest(1), Status: apphost.DeployStatusPending})
	}
	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(registry.deleted, repo+"@"+sweepDigest(1)) {
		t.Errorf("the image a deploy just named was untagged: %v", registry.deleted)
	}
}

// A repository the sweep cannot judge is left whole.
func TestRegistrySweep_LeavesWhatItCannotJudge(t *testing.T) {
	sweeper, registry, deploys := sweepFixture(t)
	sweeper.live = func(context.Context, string) (bool, error) { return false, errors.New("platform db down") }
	report, err := sweeper.Sweep(context.Background())
	if err != nil || len(registry.deleted) != 0 || report.Failed != 2 {
		t.Fatalf("store down: deleted %v, %+v %v", registry.deleted, report, err)
	}
	sweeper, registry, deploys = sweepFixture(t)
	deploys.listErr = errors.New("down")
	if report, _ := sweeper.Sweep(context.Background()); report.Failed != 1 || slices.ContainsFunc(registry.deleted, func(d string) bool { return !strings.Contains(d, "/gone@") }) {
		t.Fatalf("deploys unreadable: deleted %v, %+v", registry.deleted, report)
	}
	registry.tags["Not A Repo"] = []string{"x"}
	registry.deleted = nil
	if report, _ := sweeper.Sweep(context.Background()); report.Failed != 2 {
		t.Errorf("a repository the platform cannot have named: %+v", report)
	}
	registry.tagsErr, registry.deleted = errors.New("zot down"), nil
	if report, _ := sweeper.Sweep(context.Background()); len(registry.deleted) != 0 || report.Failed != 3 {
		t.Errorf("tags unreadable: %v %+v", registry.deleted, report)
	}
	registry.listErr = errors.New("zot down")
	if _, err := sweeper.Sweep(context.Background()); err == nil {
		t.Error("a catalog that cannot be read is an error")
	}
}

func TestRegistrySweep_CountsADeleteThatFails(t *testing.T) {
	sweeper, registry, _ := sweepFixture(t)
	registry.deleteErr = errors.New("zot down")
	if report, _ := sweeper.Sweep(context.Background()); report.Deleted != 0 || report.Failed != 3 {
		t.Errorf("%+v", report)
	}
}

type countingLeader struct {
	leading bool
	err     error
	calls   chan struct{}
}

func (c *countingLeader) IsLeader(context.Context) (bool, error) {
	c.calls <- struct{}{}
	return c.leading, c.err
}

func TestRegistrySweep_RunsOnlyWhileLeading(t *testing.T) {
	for _, leader := range []*countingLeader{{leading: false}, {err: errors.New("lock")}, {leading: true}} {
		sweeper, registry, _ := sweepFixture(t)
		leader.calls = make(chan struct{}, 4)
		sweeper.sweepIfLeader(context.Background(), leader)
		if leader.leading != (len(registry.deleted) > 0) {
			t.Errorf("leading %v err %v: deleted %v", leader.leading, leader.err, registry.deleted)
		}
	}
	sweeper, _, _ := sweepFixture(t)
	leader := &countingLeader{calls: make(chan struct{}, 4)}
	stop := sweeper.Start(context.Background(), leader, time.Hour)
	<-leader.calls
	stop()
}
