package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// shipFixture is a single host's MySQL project with one backup at 10:00 whose
// server has committed transactions up to gtid.
type shipFixture struct {
	*hostBackupFixture
	gtid    string
	flushes int
}

func newShipFixture(t *testing.T) *shipFixture {
	t.Helper()
	f := &shipFixture{hostBackupFixture: newHostBackupFixture(t), gtid: serverA + ":1-10"}
	base := f.engine.answer
	f.engine.answer = func(container, stdin string) (string, error) {
		switch {
		case strings.Contains(stdin, "gtid_executed"):
			return f.server + "\t" + f.clock.UTC().Format("2006-01-02T15:04:05Z") + "\t" + f.gtid + "\n", nil
		case strings.Contains(stdin, "FLUSH BINARY LOGS"):
			f.flushes++
		}
		return base(container, stdin)
	}
	if _, err := f.adapter.TriggerManual(context.Background(), f.inst); err != nil {
		t.Fatal(err)
	}
	f.flushes = 0
	return f
}

func (f *shipFixture) commit() { f.gtid = serverA + ":1-" + f.clock.Format("150405") }

func (f *shipFixture) restorePlans() {
	f.adapter.SetProjectRegistrar(&fakeRegistrar{store: f.adapter.instances})
	f.adapter.SetDatabaseProbe(alwaysAnswers{})
	f.adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Free,
		Config: config.TierConfig{Instances: 1, Memory: "512Mi", CPU: "1", StorageSize: "5Gi"}}})
	f.setBinlogs("excalibase-copy-mysql")
}

func TestHostMySQLShippedBinlogsCarryTheRestoreWindowToTheLastShip(t *testing.T) {
	f := newShipFixture(t)
	ctx := context.Background()
	f.tick(90 * time.Second)
	f.commit()
	if err := f.adapter.ShipBinlogs(ctx, f.inst); err != nil {
		t.Fatalf("ShipBinlogs: %v", err)
	}
	if f.flushes != 1 {
		t.Fatalf("flushes %d, want 1", f.flushes)
	}
	shipped := f.keys("backups/p1/mysql/binlogs/" + serverA + "/")
	if !strings.HasSuffix(strings.Join(shipped, ","), "binlog.000006.gz,backups/p1/mysql/binlogs/"+serverA+"/shipped.json") {
		t.Fatalf("shipped %v", shipped)
	}
	window, err := f.adapter.RestoreWindow(ctx, f.inst)
	if err != nil || window == nil || !window.Latest.Equal(time.Date(2026, 10, 11, 10, 1, 30, 0, time.UTC)) {
		t.Fatalf("window %+v %v, want it to reach the ship at 10:01:30", window, err)
	}

	f.restorePlans()
	target := &domain.ZonedTime{Time: time.Date(2026, 10, 11, 10, 1, 10, 0, time.UTC)}
	if _, err := f.adapter.Restore(ctx, f.inst, domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "copy", TargetTime: target}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	copied := strings.Join(f.engine.copied["excalibase-copy-mysql"], ",")
	if !strings.Contains(copied, "excalibase-restore/dump.sql.gz") || !strings.Contains(copied, "excalibase-restore/binlog.000006.gz") ||
		strings.Contains(copied, "binlog.000005") {
		t.Fatalf("copied %s", copied)
	}
	loads := f.streamer.ran("excalibase-copy-mysql: ")
	if len(loads) != 1 || !strings.Contains(loads[0], "--stop-datetime='2026-10-11 10:01:10'") {
		t.Fatalf("load %v", loads)
	}
}

func TestHostMySQLShipWithoutNewTransactionsMovesTheWindowWithoutAFlush(t *testing.T) {
	f := newShipFixture(t)
	ctx := context.Background()
	f.tick(time.Minute)
	f.commit()
	if err := f.adapter.ShipBinlogs(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.tick(time.Minute)
	if err := f.adapter.ShipBinlogs(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	if f.flushes != 1 {
		t.Fatalf("an idle server was flushed: %d flushes", f.flushes)
	}
	window, _ := f.adapter.RestoreWindow(ctx, f.inst)
	if window == nil || !window.Latest.Equal(time.Date(2026, 10, 11, 10, 2, 0, 0, time.UTC)) {
		t.Fatalf("window %+v", window)
	}
	if err := f.adapter.CheckRestoreTarget(ctx, f.inst, domain.RestoreRequest{TargetTime: &domain.ZonedTime{Time: f.clock.Add(-10 * time.Second)}}); err != nil {
		t.Fatalf("a point before the last ship was refused: %v", err)
	}
}

func TestHostMySQLIdleShipAfterABackupNeedsNoBinlogToReachIt(t *testing.T) {
	f := newShipFixture(t)
	ctx := context.Background()
	f.tick(time.Minute)
	if err := f.adapter.ShipBinlogs(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.tick(time.Minute)
	if _, err := f.adapter.TriggerManual(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.tick(time.Minute)
	if err := f.adapter.ShipBinlogs(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.restorePlans()
	target := &domain.ZonedTime{Time: f.clock.Add(-30 * time.Second)}
	if _, err := f.adapter.Restore(ctx, f.inst, domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "copy", TargetTime: target}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if copied := strings.Join(f.engine.copied["excalibase-copy-mysql"], ","); strings.Contains(copied, "binlog") {
		t.Fatalf("nothing was committed after the backup, yet binlogs were loaded: %s", copied)
	}
}

func TestHostMySQLPointAfterTheLastShipIsRefused(t *testing.T) {
	f := newShipFixture(t)
	ctx := context.Background()
	f.tick(time.Minute)
	f.commit()
	if err := f.adapter.ShipBinlogs(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	late := &domain.ZonedTime{Time: f.clock.Add(time.Second)}
	if err := f.adapter.CheckRestoreTarget(ctx, f.inst, domain.RestoreRequest{TargetTime: late}); !errors.Is(err, ErrRestoreTargetInvalid) {
		t.Fatalf("a point after the last ship: %v", err)
	}
}

func TestHostMySQLShipOfAServerWithoutABackupWritesNothing(t *testing.T) {
	f := newShipFixture(t)
	ctx := context.Background()
	f.server = serverB
	f.tick(time.Minute)
	if err := f.adapter.ShipBinlogs(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	if keys := f.keys("backups/p1/mysql/binlogs/" + serverB + "/"); len(keys) != 0 || f.flushes != 0 {
		t.Fatalf("a server with no backup to replay onto shipped %v (%d flushes)", keys, f.flushes)
	}
}

func TestHostMySQLShipOfAStoppedProjectIsSkipped(t *testing.T) {
	f := newShipFixture(t)
	f.engine.containers["excalibase-p1-mysql"] = false
	if err := f.adapter.ShipBinlogs(context.Background(), f.inst); err != nil {
		t.Fatalf("a paused project is skipped, not an error: %v", err)
	}
	if keys := f.keys("backups/p1/mysql/binlogs/" + serverA + "/shipped.json"); len(keys) != 0 {
		t.Fatalf("shipped %v", keys)
	}
}

func TestHostMySQLRetentionDropsTheShipMarkOfAServerNoRunNeeds(t *testing.T) {
	f := newShipFixture(t)
	ctx := context.Background()
	f.tick(time.Minute)
	f.commit()
	if err := f.adapter.ShipBinlogs(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.server = serverB
	f.tick(10 * 24 * time.Hour)
	if _, err := f.adapter.RunScheduled(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.tick(10 * 24 * time.Hour)
	if _, err := f.adapter.RunScheduled(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adapter.PurgeExpired(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	if keys := f.keys("backups/p1/mysql/binlogs/" + serverA + "/"); len(keys) != 0 {
		t.Fatalf("server A has no kept run, yet kept %v", keys)
	}
}

func TestHostMySQLBinlogSweepShipsRunningBackedUpProjectsOnly(t *testing.T) {
	f := newShipFixture(t)
	ctx := context.Background()
	paused := *f.inst
	paused.ProjectID, paused.Status = "p2", string(domain.StatusPaused)
	if err := f.adapter.instances.Create(&paused); err != nil {
		t.Fatal(err)
	}
	f.tick(time.Minute)
	f.commit()
	NewHostMySQLBinlogSweep(f.adapter, f.adapter.instances).Sweep(ctx)
	if keys := f.keys("backups/p1/mysql/binlogs/" + serverA + "/shipped.json"); len(keys) != 1 {
		t.Fatalf("the running project was not shipped: %v", f.keys("backups/"))
	}
	if keys := f.keys("backups/p2/"); len(keys) != 0 {
		t.Fatalf("the paused project was shipped: %v", keys)
	}
}
