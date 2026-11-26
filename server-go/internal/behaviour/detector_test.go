package behaviour

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

type fakeHistory struct {
	minutes []netmeter.Sample
	volumes []AppVolume
	err     error
	since   time.Time
}

func (f *fakeHistory) RecentNetMinutes(_ context.Context, since time.Time) ([]netmeter.Sample, error) {
	f.since = since
	return f.minutes, f.err
}
func (f *fakeHistory) NetVolumes(context.Context, time.Time) ([]AppVolume, error) {
	return f.volumes, f.err
}

type fakeDirectory struct {
	apps     []Subject
	projects map[string]Subject
	err      error
}

func (f fakeDirectory) ReviewDirectory(context.Context) ([]Subject, map[string]Subject, error) {
	return f.apps, f.projects, f.err
}

type recordedReviews struct {
	findings []Finding
	pruned   []time.Time
	err      error
}

func (r *recordedReviews) RecordFinding(_ context.Context, f Finding, _ time.Time, _ Rules) (Outcome, error) {
	r.findings = append(r.findings, f)
	return OutcomeOpened, r.err
}
func (r *recordedReviews) PruneReviews(_ context.Context, now time.Time, _ int) error {
	r.pruned = append(r.pruned, now)
	return nil
}

func ownerProjects() (map[string]Subject, []Subject) {
	projects := map[string]Subject{
		"p1": {OwnerID: "u1", OrgID: "o1", ProjectID: "p1", ProjectName: "one", Tier: "free"},
		"p2": {OwnerID: "u1", OrgID: "o1", ProjectID: "p2", ProjectName: "two", Tier: "free"},
		"p3": {OwnerID: "u2", OrgID: "o2", ProjectID: "p3", ProjectName: "three", Tier: "free"},
	}
	var apps []Subject
	for _, a := range []struct{ project, app string }{{"p1", "a"}, {"p1", "b"}, {"p2", "c"}, {"p3", "d"}} {
		s := projects[a.project]
		s.AppID, s.AppName = a.app, "name-"+a.app
		apps = append(apps, s)
	}
	return projects, apps
}

func minutesFor(project, app string, txPercent, rxOverTx float64) []netmeter.Sample {
	out := series(60, txPercent, rxOverTx)
	for i := range out {
		out[i].ProjectID, out[i].AppID = project, app
	}
	return out
}

func newDetector(history *fakeHistory, dir fakeDirectory, reviews *recordedReviews, clock *time.Time) *Detector {
	detector := NewDetector(DetectorDeps{History: history, Directory: dir, Reviews: reviews}, testConfig())
	detector.now = func() time.Time { return *clock }
	return detector
}

func TestDetector_RaisesPerAppAndPerOwnerFindingsWithSnapshots(t *testing.T) {
	projects, apps := ownerProjects()
	var minutes []netmeter.Sample
	minutes = append(minutes, minutesFor("p1", "a", 90, 0.1)...) // one app is over the line
	for _, c := range [][2]string{{"p1", "b"}, {"p2", "c"}} {    // the owner's other apps are modest, but add up
		minutes = append(minutes, minutesFor(c[0], c[1], 25, 0.1)...)
	}
	minutes = append(minutes, minutesFor("p3", "d", 5, 1)...) // another owner, quiet
	history := &fakeHistory{minutes: minutes}
	reviews := &recordedReviews{}
	clock := now
	newDetector(history, fakeDirectory{apps: apps, projects: projects}, reviews, &clock).Sample(context.Background())

	var appLevel, ownerLevel []Finding
	for _, f := range reviews.findings {
		if f.AppID == "" {
			ownerLevel = append(ownerLevel, f)
		} else {
			appLevel = append(appLevel, f)
		}
	}
	if len(appLevel) != 1 || appLevel[0].AppID != "a" || appLevel[0].Signal != SignalSustainedEgress || appLevel[0].AppName != "name-a" || appLevel[0].ProjectName != "one" {
		t.Fatalf("app level = %+v", appLevel)
	}
	if len(ownerLevel) != 1 || ownerLevel[0].OwnerID != "u1" || ownerLevel[0].Signal != SignalSustainedEgress || ownerLevel[0].Evidence["apps"] != 3 {
		t.Fatalf("owner level = %+v: u1's three apps add up to 140%% of one Free allowance, u2's one app does not", ownerLevel)
	}
	if history.since != now.Add(-60*time.Minute) {
		t.Fatalf("the window starts %v", history.since)
	}
}

func TestDetector_OwnerVolumeCountsDeletedAppsOfProjectsThatStillExist(t *testing.T) {
	projects, apps := ownerProjects()
	day := freeRef * 86400
	history := &fakeHistory{volumes: []AppVolume{
		{ProjectID: "p1", AppID: "a", Totals: Totals{TxDay: day * 0.10}},
		{ProjectID: "p1", AppID: "gone", Totals: Totals{TxDay: day * 0.25}},  // deleted app, project still there
		{ProjectID: "p-deleted", AppID: "x", Totals: Totals{TxDay: day * 5}}, // project gone: nobody to attribute it to
	}}
	reviews := &recordedReviews{}
	clock := now
	newDetector(history, fakeDirectory{apps: apps, projects: projects}, reviews, &clock).Sample(context.Background())
	if len(reviews.findings) != 1 || reviews.findings[0].Signal != SignalVolumeDaily || reviews.findings[0].OwnerID != "u1" || reviews.findings[0].AppID != "" {
		t.Fatalf("findings = %+v: only the owner total (35%%) crosses the daily line, neither app does", reviews.findings)
	}
}

func TestDetector_WaitsBetweenEvaluations(t *testing.T) {
	projects, apps := ownerProjects()
	history := &fakeHistory{minutes: minutesFor("p1", "a", 95, 0.1)}
	reviews := &recordedReviews{}
	clock := now
	detector := newDetector(history, fakeDirectory{apps: apps, projects: projects}, reviews, &clock)
	detector.Sample(context.Background())
	first := len(reviews.findings)
	clock = clock.Add(time.Minute)
	detector.Sample(context.Background())
	if len(reviews.findings) != first {
		t.Fatal("a second look inside the evaluation interval does nothing")
	}
	clock = clock.Add(5 * time.Minute)
	detector.Sample(context.Background())
	if len(reviews.findings) == first {
		t.Fatal("after the interval it looks again")
	}
}

func TestDetector_ErrorsAreSurvivedAndNothingIsRecorded(t *testing.T) {
	projects, apps := ownerProjects()
	clock := now
	for name, d := range map[string]*Detector{
		"history":   newDetector(&fakeHistory{err: errors.New("db")}, fakeDirectory{apps: apps, projects: projects}, &recordedReviews{}, &clock),
		"directory": newDetector(&fakeHistory{minutes: minutesFor("p1", "a", 95, 0.1)}, fakeDirectory{err: errors.New("db")}, &recordedReviews{}, &clock),
	} {
		d.Sample(context.Background())
		if r := d.deps.Reviews.(*recordedReviews); len(r.findings) != 0 {
			t.Errorf("%s error still recorded %+v", name, r.findings)
		}
	}
}

func TestDetector_AReviewStoreErrorDoesNotStopTheRest(t *testing.T) {
	projects, apps := ownerProjects()
	minutes := append(minutesFor("p1", "a", 95, 0.1), minutesFor("p3", "d", 95, 0.1)...)
	reviews := &recordedReviews{err: errors.New("db")}
	clock := now
	newDetector(&fakeHistory{minutes: minutes}, fakeDirectory{apps: apps, projects: projects}, reviews, &clock).Sample(context.Background())
	if len(reviews.findings) < 2 {
		t.Fatalf("every finding is attempted: %d", len(reviews.findings))
	}
}

func TestDetector_PrunesDismissedReviewsOncePerDay(t *testing.T) {
	projects, apps := ownerProjects()
	reviews := &recordedReviews{}
	clock := now
	detector := newDetector(&fakeHistory{}, fakeDirectory{apps: apps, projects: projects}, reviews, &clock)
	for _, step := range []time.Duration{0, 10 * time.Minute, 12 * time.Hour, 25 * time.Hour} {
		clock = now.Add(step)
		detector.Sample(context.Background())
	}
	if len(reviews.pruned) != 2 {
		t.Fatalf("pruned %d times", len(reviews.pruned))
	}
}

func TestDetector_NeverActsOnAnApp(t *testing.T) {
	// The detector's whole surface is History, Directory and Reviews: nothing that can stop or suspend.
	var _ History = (*fakeHistory)(nil)
	var _ Directory = fakeDirectory{}
	var _ Reviews = (*recordedReviews)(nil)
}
