package hostlogs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

const gib = uint64(1) << 30

// fakeDisk answers free and total bytes, one reading at a time.
type fakeDisk struct {
	free, total uint64
	err         error
	reads       int
}

func (d *fakeDisk) read(string) (uint64, uint64, error) {
	d.reads++
	return d.free, d.total, d.err
}

func newTestGuard(disk *fakeDisk, clock *time.Time) *DiskGuard {
	guard := NewDiskGuard("/var/lib/excalibase", 10)
	guard.statfs = disk.read
	guard.now = func() time.Time { return *clock }
	return guard
}

// EXC-678: shipping pauses below 10% or 2 GiB free and resumes only at 15%
// and 3 GiB, so a disk hovering at the line does not flap.
func TestDiskGuardPausesAndResumesWithHysteresis(t *testing.T) {
	clock := now
	disk := &fakeDisk{free: 50 * gib, total: 100 * gib}
	guard := newTestGuard(disk, &clock)
	steps := []struct {
		free uint64
		want bool
	}{
		{50 * gib, false},
		{11 * gib, false},
		{9 * gib, true},  // under 10%
		{12 * gib, true}, // over 10%, under the 15% to resume
		{16 * gib, false},
		{9 * gib, true},
	}
	for i, step := range steps {
		disk.free = step.free
		clock = clock.Add(diskCheckEvery)
		if got := guard.Paused(); got != step.want {
			t.Fatalf("step %d (%d GiB free): paused = %v, want %v", i, step.free/gib, got, step.want)
		}
	}
}

// A small disk pauses on the absolute floor even above the percentage.
func TestDiskGuardKeepsAnAbsoluteFloor(t *testing.T) {
	clock := now
	disk := &fakeDisk{free: 1 * gib, total: 8 * gib}
	guard := newTestGuard(disk, &clock)
	if !guard.Paused() {
		t.Error("1 GiB free of 8 (12%) did not pause")
	}
	disk.free = 2*gib + gib/2
	clock = clock.Add(diskCheckEvery)
	if !guard.Paused() {
		t.Error("resumed under the 3 GiB resume floor")
	}
}

func TestDiskGuardReadsAtMostEveryInterval(t *testing.T) {
	clock := now
	disk := &fakeDisk{free: 50 * gib, total: 100 * gib}
	guard := newTestGuard(disk, &clock)
	for i := 0; i < 5; i++ {
		guard.Paused()
	}
	if disk.reads != 1 {
		t.Errorf("%d reads within the interval", disk.reads)
	}
	clock = clock.Add(diskCheckEvery)
	guard.Paused()
	if disk.reads != 2 {
		t.Errorf("%d reads after the interval", disk.reads)
	}
}

// A disk that cannot be read does not stop shipping: the disk alert covers it.
func TestDiskGuardDoesNotPauseOnAFailedRead(t *testing.T) {
	clock := now
	disk := &fakeDisk{err: errors.New("no such file")}
	guard := newTestGuard(disk, &clock)
	if guard.Paused() {
		t.Error("paused on a failed read")
	}
	disk.err, disk.free, disk.total = nil, gib/2, 100*gib
	clock = clock.Add(diskCheckEvery)
	if !guard.Paused() {
		t.Fatal("not paused")
	}
	// Once paused, a failed read keeps the last state.
	disk.err = errors.New("gone")
	clock = clock.Add(diskCheckEvery)
	if !guard.Paused() {
		t.Error("a failed read resumed shipping")
	}
	if (&DiskGuard{}).Paused() {
		t.Error("a zero guard paused")
	}
	var none *DiskGuard
	if none.Paused() {
		t.Error("a nil guard paused")
	}
}

func TestNewDiskGuardBoundsThePercentage(t *testing.T) {
	if got := NewDiskGuard("/x", 0); got.minFreePercent != defaultMinFreePercent {
		t.Errorf("0%% became %v", got.minFreePercent)
	}
	if got := NewDiskGuard("/x", 80); got.minFreePercent != maxMinFreePercent {
		t.Errorf("80%% became %v", got.minFreePercent)
	}
}

// While paused nothing new is followed and open follows stop; resumed, they start again.
func TestScanStopsAndStartsFollowsWithTheDisk(t *testing.T) {
	engine := &fakeEngine{containers: []enginestats.Container{{ID: "db-a", Name: provisioner.DatabaseContainerName("proj-a"), State: "running"}},
		logs: map[string][]enginestats.LogLine{}}
	blocking := &blockingEngine{fakeEngine: engine}
	clock := now
	disk := &fakeDisk{free: gib / 2, total: 100 * gib}
	shipper := newTestShipper(engine, &fakeCursors{}, &fakePusher{}, features.NewStatic(), dockerProject("proj-a", domain.Free, "db-a"))
	shipper.deps.Engine = blocking
	shipper.deps.Disk = newTestGuard(disk, &clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer shipper.Stop()

	if err := shipper.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if shipper.following() != 0 {
		t.Fatal("followed with the disk nearly full")
	}
	disk.free = 50 * gib
	clock = clock.Add(diskCheckEvery)
	if err := shipper.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if shipper.following() != 1 {
		t.Fatal("did not resume")
	}
	disk.free = gib / 2
	clock = clock.Add(diskCheckEvery)
	if err := shipper.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if shipper.following() != 0 {
		t.Error("an open follow kept going with the disk nearly full")
	}
}

// A batch read before the pause is not pushed while paused, and the cursor stays.
func TestFlushHoldsTheBatchWhilePaused(t *testing.T) {
	clock := now
	disk := &fakeDisk{free: gib / 2, total: 100 * gib}
	cursors, pusher := &fakeCursors{}, &fakePusher{}
	shipper := newTestShipper(&fakeEngine{}, cursors, pusher, features.NewStatic())
	shipper.deps.Disk = newTestGuard(disk, &clock)
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour, DailyBytes: 1 << 20}
	b := newBatch(target, 0, 0)
	shipper.add(b, line(1, "held"))
	if err := shipper.flush(context.Background(), b); !errors.Is(err, errDiskLow) {
		t.Fatalf("flush = %v", err)
	}
	if len(pusher.pushes) != 0 || len(cursors.saved) != 0 {
		t.Errorf("pushed %d, saved %v", len(pusher.pushes), cursors.saved)
	}
}

// blockingEngine follows a log that never ends until the follow is stopped.
type blockingEngine struct{ *fakeEngine }

func (b *blockingEngine) FollowLogs(ctx context.Context, _ string, _ time.Time) (Lines, error) {
	return &blockingLines{ctx: ctx}, nil
}

type blockingLines struct{ ctx context.Context }

func (l *blockingLines) Next() (enginestats.LogLine, error) {
	<-l.ctx.Done()
	return enginestats.LogLine{}, l.ctx.Err()
}

func (l *blockingLines) Close() error { return nil }
