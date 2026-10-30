package hostlogs

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

type fakeLines struct {
	lines []enginestats.LogLine
	err   error
}

func (f *fakeLines) Next() (enginestats.LogLine, error) {
	if len(f.lines) == 0 {
		if f.err != nil {
			return enginestats.LogLine{}, f.err
		}
		return enginestats.LogLine{}, io.EOF
	}
	line := f.lines[0]
	f.lines = f.lines[1:]
	return line, nil
}

func (f *fakeLines) Close() error { return nil }

type fakeEngine struct {
	mu         sync.Mutex
	containers []enginestats.Container
	logs       map[string][]enginestats.LogLine
	since      map[string]time.Time
	followErr  error
}

func (e *fakeEngine) Managed(context.Context) ([]enginestats.Container, error) {
	return e.containers, nil
}

func (e *fakeEngine) FollowLogs(_ context.Context, ref string, since time.Time) (Lines, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.since == nil {
		e.since = map[string]time.Time{}
	}
	e.since[ref] = since
	if e.followErr != nil {
		return nil, e.followErr
	}
	return &fakeLines{lines: append([]enginestats.LogLine(nil), e.logs[ref]...)}, nil
}

type fakeCursors struct {
	mu     sync.Mutex
	saved  map[string]int64
	kept   []string
	failed error
}

func (c *fakeCursors) HostLogCursors(context.Context) (map[string]int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int64{}
	for id, ns := range c.saved {
		out[id] = ns
	}
	return out, c.failed
}

func (c *fakeCursors) SaveHostLogCursor(_ context.Context, containerID, _ string, ns int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saved == nil {
		c.saved = map[string]int64{}
	}
	c.saved[containerID] = ns
	return nil
}

func (c *fakeCursors) PruneHostLogCursors(_ context.Context, keep []string) error {
	c.kept = keep
	return nil
}

type fakePusher struct {
	mu      sync.Mutex
	pushes  [][]Stream
	failFor int
}

func (p *fakePusher) Push(_ context.Context, streams []Stream) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failFor > 0 {
		p.failFor--
		return errors.New("loki answered 503")
	}
	p.pushes = append(p.pushes, streams)
	return nil
}

func (p *fakePusher) lines() []string {
	var out []string
	for _, push := range p.pushes {
		for _, stream := range push {
			for _, entry := range stream.Entries {
				out = append(out, entry.Line)
			}
		}
	}
	return out
}

type instances []*domain.DatabaseInstance

func (i instances) FindAll() ([]*domain.DatabaseInstance, error) { return i, nil }

func dockerProject(id string, tier domain.TierType, containerID string) *domain.DatabaseInstance {
	return &domain.DatabaseInstance{ProjectID: id, DeploymentMode: domain.ModeDocker, Namespace: containerID, Tier: tier, DBType: domain.PostgreSQL}
}

func line(sec int, text string) enginestats.LogLine {
	return enginestats.LogLine{At: now.Add(time.Duration(sec) * time.Second), Text: text}
}

func newTestShipper(engine *fakeEngine, cursors *fakeCursors, pusher *fakePusher, flags features.Flags, projects ...*domain.DatabaseInstance) *Shipper {
	return New(Deps{
		Engine: engine, Cursors: cursors, Pusher: pusher, Instances: instances(projects), Flags: flags,
		Retention:  config.LogRetention{Default: 168 * time.Hour, ByPlan: map[domain.TierType]time.Duration{domain.Free: 24 * time.Hour}},
		DailyBytes: 1 << 20, Now: func() time.Time { return now }, RetryDelay: time.Millisecond,
	})
}

// EXC-672: only running project databases are shipped; apps, the platform's
// and unattributed containers never are.
func TestTargetsAreTheRunningProjectContainersOnly(t *testing.T) {
	engine := &fakeEngine{containers: []enginestats.Container{
		{ID: "db-a", Name: provisioner.DatabaseContainerName("proj-a"), State: "running"},
		{ID: "app-b", Name: "excalibase-app-x", State: "running", Labels: map[string]string{"excalibase.project": "proj-b", "excalibase.component": "app"}},
		{ID: "db-c", Name: provisioner.DatabaseContainerName("proj-c"), State: "exited"},
		{ID: "stray", Name: "excalibase-stray", State: "running"},
		{ID: "evil", Name: "excalibase-app-y", State: "running", Labels: map[string]string{"excalibase.project": `proj-a",x="`, "excalibase.component": "app"}},
	}}
	shipper := newTestShipper(engine, &fakeCursors{}, &fakePusher{}, features.NewStatic(),
		dockerProject("proj-a", domain.Free, "db-a"), dockerProject("proj-c", domain.Free, "db-c"))
	targets, seen, err := shipper.targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Only what the project log search reads: the database, not app-b.
	if len(targets) != 1 || targets[0].Labels != (Labels{ProjectID: "proj-a", Component: "database", Container: "excalibase-proj-a-postgres"}) {
		t.Errorf("targets = %+v", targets)
	}
	if len(seen) != 5 {
		t.Errorf("seen = %v, want every managed container kept for its cursor", seen)
	}
}

// The plan label rides only with per-plan retention on, as on Kubernetes.
func TestTierLabelFollowsTheLogRetentionFlag(t *testing.T) {
	engine := &fakeEngine{containers: []enginestats.Container{{ID: "db-a", Name: provisioner.DatabaseContainerName("proj-a"), State: "running"}}}
	project := dockerProject("proj-a", domain.Free, "db-a")
	off := newTestShipper(engine, &fakeCursors{}, &fakePusher{}, features.NewStatic(), project)
	targets, _, _ := off.targets(context.Background())
	if targets[0].Labels.Tier != "" || targets[0].Retention != 168*time.Hour {
		t.Errorf("off: %+v", targets[0])
	}
	on := newTestShipper(engine, &fakeCursors{}, &fakePusher{}, features.NewStatic(features.LogRetention), project)
	targets, _, _ = on.targets(context.Background())
	if targets[0].Labels.Tier != "free" || targets[0].Retention != 24*time.Hour {
		t.Errorf("on: %+v", targets[0])
	}
}

func TestShipStreamSkipsLinesUpToTheCursorAndSavesTheNewOne(t *testing.T) {
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": {line(1, "one"), line(2, "two"), line(3, "three"), line(4, "four")}}}
	cursors, pusher := &fakeCursors{}, &fakePusher{}
	shipper := newTestShipper(engine, cursors, pusher, features.NewStatic())
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: 168 * time.Hour}

	cursor, err := shipper.shipStream(context.Background(), target, now.Add(2*time.Second).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pusher.lines(), ","); got != "three,four" {
		t.Errorf("pushed %s", got)
	}
	if cursor != now.Add(4*time.Second).UnixNano() || cursors.saved["db-a"] != cursor {
		t.Errorf("cursor %d saved %d", cursor, cursors.saved["db-a"])
	}
	if !engine.since["db-a"].Equal(now.Add(2 * time.Second)) {
		t.Errorf("since %v", engine.since["db-a"])
	}
	if pusher.pushes[0][0].Labels != target.Labels || pusher.pushes[0][0].Entries[0].At != now.Add(3*time.Second).UnixNano() {
		t.Errorf("stream = %+v", pusher.pushes[0][0])
	}
}

// With no cursor, only what the plan still keeps is read back.
func TestShipStreamBackfillsNoFurtherThanTheRetention(t *testing.T) {
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": {line(-25*3600, "too old"), line(-3600, "kept")}}}
	pusher := &fakePusher{}
	shipper := newTestShipper(engine, &fakeCursors{}, pusher, features.NewStatic())
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: 24 * time.Hour}
	if _, err := shipper.shipStream(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	if !engine.since["db-a"].Equal(now.Add(-24 * time.Hour)) {
		t.Errorf("since %v", engine.since["db-a"])
	}
	if got := strings.Join(pusher.lines(), ","); got != "kept" {
		t.Errorf("pushed %s", got)
	}
}

// Loki down: the batch is retried, and the cursor moves only after it is stored.
func TestShipStreamRetriesAPushBeforeMovingTheCursor(t *testing.T) {
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": {line(1, "one")}}}
	cursors, pusher := &fakeCursors{}, &fakePusher{failFor: 2}
	shipper := newTestShipper(engine, cursors, pusher, features.NewStatic())
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour}
	if _, err := shipper.shipStream(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	if len(pusher.pushes) != 1 || cursors.saved["db-a"] != now.Add(time.Second).UnixNano() {
		t.Errorf("pushes %d cursor %d", len(pusher.pushes), cursors.saved["db-a"])
	}
}

func TestShipStreamGivesUpTheBatchWhenStopped(t *testing.T) {
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": {line(1, "one")}}}
	cursors, pusher := &fakeCursors{}, &fakePusher{failFor: 1 << 30}
	shipper := newTestShipper(engine, cursors, pusher, features.NewStatic())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour}
	if _, err := shipper.shipStream(ctx, target, 0); err == nil {
		t.Error("a stopped shipper reported success")
	}
	if _, saved := cursors.saved["db-a"]; saved {
		t.Error("the cursor moved past lines Loki never stored")
	}
}

// Past the day's budget one notice is stored and the rest skipped; the cursor still moves.
func TestShipStreamStopsAtTheDailyBudget(t *testing.T) {
	big := strings.Repeat("x", 600<<10)
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": {line(1, big), line(2, big), line(3, big)}}}
	cursors, pusher := &fakeCursors{}, &fakePusher{}
	shipper := newTestShipper(engine, cursors, pusher, features.NewStatic())
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour, DailyBytes: 20 << 10}
	if _, err := shipper.shipStream(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	lines := pusher.lines()
	if len(lines) != 2 || !strings.Contains(lines[1], "daily log budget") {
		t.Fatalf("pushed %d lines, last %.80q", len(lines), lines[len(lines)-1])
	}
	if cursors.saved["db-a"] != now.Add(3*time.Second).UnixNano() {
		t.Errorf("cursor %d, want past the skipped lines", cursors.saved["db-a"])
	}
}

// Line text is cut before it is held or sent.
func TestShipStreamCutsLongLines(t *testing.T) {
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": {line(1, strings.Repeat("é", 20<<10))}}}
	pusher := &fakePusher{}
	shipper := newTestShipper(engine, &fakeCursors{}, pusher, features.NewStatic())
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour}
	if _, err := shipper.shipStream(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	if got := pusher.lines()[0]; len(got) > 16<<10 || !strings.HasPrefix(got, "é") {
		t.Errorf("line of %d bytes", len(got))
	}
}

// Scan follows new containers, stops the gone, and keeps cursors of every container the engine has.
func TestScanStartsAndStopsFollowers(t *testing.T) {
	engine := &endlessEngine{&fakeEngine{containers: []enginestats.Container{{ID: "db-a", Name: provisioner.DatabaseContainerName("proj-a"), State: "running"}}}}
	cursors := &fakeCursors{}
	shipper := New(Deps{Engine: engine, Cursors: cursors, Pusher: &fakePusher{}, Flags: features.NewStatic(),
		Instances: instances{dockerProject("proj-a", domain.Free, "db-a")}, DailyBytes: 1 << 30, Now: func() time.Time { return now }, RetryDelay: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := shipper.Scan(ctx, true); err != nil {
		t.Fatal(err)
	}
	if shipper.following() != 1 || len(cursors.kept) != 1 || cursors.kept[0] != "db-a" {
		t.Fatalf("following %d kept %v", shipper.following(), cursors.kept)
	}
	engine.containers = nil
	if err := shipper.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if shipper.following() != 0 {
		t.Errorf("following %d after the container went", shipper.following())
	}
	shipper.Stop()
}

func TestValidLabels(t *testing.T) {
	good := Labels{ProjectID: "proj-abc", Component: "database", Container: "excalibase-proj-abc-postgres", Tier: "free"}
	if !good.valid() {
		t.Error("good labels refused")
	}
	for _, bad := range []Labels{
		{ProjectID: "Proj", Component: "database", Container: "c"},
		{ProjectID: "proj-a", Component: "platform", Container: "c"},
		{ProjectID: "proj-a", Component: "database", Container: `c"}`},
		{ProjectID: "proj-a", Component: "database", Container: "c", Tier: "gold"},
		{ProjectID: "", Component: "database", Container: "c"},
	} {
		if bad.valid() {
			t.Errorf("%+v accepted", bad)
		}
	}
}

// Two lines the engine stamped with the same instant are both kept.
func TestShipStreamKeepsLinesOfTheSameInstant(t *testing.T) {
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": {line(1, "one"), line(1, "two"), line(2, "three")}}}
	pusher := &fakePusher{}
	shipper := newTestShipper(engine, &fakeCursors{}, pusher, features.NewStatic())
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour}
	if _, err := shipper.shipStream(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pusher.lines(), ","); got != "one,two,three" {
		t.Errorf("pushed %s", got)
	}
}

// An engine answering no containers at all does not wipe every cursor.
func TestScanKeepsCursorsWhenTheEngineListsNothing(t *testing.T) {
	cursors := &fakeCursors{kept: []string{"untouched"}}
	shipper := newTestShipper(&fakeEngine{}, cursors, &fakePusher{}, features.NewStatic())
	if err := shipper.Scan(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(cursors.kept) != 1 || cursors.kept[0] != "untouched" {
		t.Errorf("pruned to %v", cursors.kept)
	}
}

// A container back before its old follow has stopped waits for it.
func TestScanWaitsForAStoppingFollow(t *testing.T) {
	shipper := newTestShipper(&fakeEngine{}, &fakeCursors{}, &fakePusher{}, features.NewStatic())
	old := &follower{cancel: func() {}, done: make(chan struct{})}
	shipper.stopping["db-a"] = old
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shipper.reconcile(ctx, []Target{target}, []string{"db-a"}, nil)
	if shipper.following() != 0 {
		t.Fatal("a second follow started beside the stopping one")
	}
	close(old.done)
	shipper.reconcile(ctx, []Target{target}, []string{"db-a"}, nil)
	if shipper.following() != 1 {
		t.Error("the follow did not start once the old one stopped")
	}
	shipper.Stop()
	shipper.reconcile(ctx, []Target{target}, []string{"db-a"}, nil)
	if shipper.following() != 0 {
		t.Error("a stopped shipper started a follow")
	}
}

// A push never grows past batchBytes, under Loki's per-stream burst.
func TestShipStreamBoundsEachPushByBytes(t *testing.T) {
	var lines []enginestats.LogLine
	for i := 0; i < 200; i++ {
		lines = append(lines, line(i-300, strings.Repeat("y", 16<<10)))
	}
	pusher := &fakePusher{}
	shipper := newTestShipper(&fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": lines}}, &fakeCursors{}, pusher, features.NewStatic())
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour, DailyBytes: 1 << 30}
	if _, err := shipper.shipStream(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, push := range pusher.pushes {
		size := 0
		for _, entry := range push[0].Entries {
			size += len(entry.Line)
		}
		if size > batchBytes+16<<10 {
			t.Errorf("a push of %d bytes", size)
		}
		total += len(push[0].Entries)
	}
	if total != 200 || len(pusher.pushes) < 3 {
		t.Errorf("%d lines in %d pushes", total, len(pusher.pushes))
	}
}

// A stamp from the future is held to now plus the skew, so the cursor cannot run ahead.
func TestShipStreamHoldsAFutureStampToNow(t *testing.T) {
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": {{At: now.Add(100 * 365 * 24 * time.Hour), Text: "from 2126"}}}}
	cursors := &fakeCursors{}
	shipper := newTestShipper(engine, cursors, &fakePusher{}, features.NewStatic())
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour}
	if _, err := shipper.shipStream(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	if cursors.saved["db-a"] != now.Add(maxClockSkew).UnixNano() {
		t.Errorf("cursor %v", time.Unix(0, cursors.saved["db-a"]))
	}
}

// A container that stopped keeps its follow until the engine ends the stream,
// so its last lines (often the crash) are stored; one that went is let go.
func TestScanLeavesAStoppedContainersFollowToFinish(t *testing.T) {
	shipper := newTestShipper(&fakeEngine{}, &fakeCursors{}, &fakePusher{}, features.NewStatic())
	run := &follower{cancel: func() { t.Error("a stopped container's follow was cancelled") }, done: make(chan struct{})}
	shipper.followers["db-a"] = run
	shipper.reconcile(context.Background(), nil, []string{"db-a"}, nil)
	if shipper.following() != 1 {
		t.Fatal("the follow was dropped")
	}
	cancelled := false
	run.cancel = func() { cancelled = true }
	shipper.reconcile(context.Background(), nil, nil, nil)
	if !cancelled || shipper.following() != 0 {
		t.Errorf("a gone container's follow: cancelled %v following %d", cancelled, shipper.following())
	}
}

// Told to stop, a follow stores what it already read.
func TestShipStreamStoresWhatItReadWhenStopped(t *testing.T) {
	pusher := &fakePusher{}
	shipper := New(Deps{Engine: endlessEngine{&fakeEngine{}}, Cursors: &fakeCursors{}, Pusher: pusher, Instances: instances{},
		Flags: features.NewStatic(), DailyBytes: 1 << 30, Now: func() time.Time { return now }, FlushEvery: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour}
	if _, err := shipper.shipStream(ctx, target, 0); err == nil {
		t.Error("a stopped follow reported a clean end")
	}
	if len(pusher.lines()) == 0 {
		t.Error("nothing read before the stop was stored")
	}
}
