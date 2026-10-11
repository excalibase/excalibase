package service

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-592: a single host backs its MySQL projects up itself, on the plan's
// interval, and only those that are running with backups on.
func TestHostMySQLSweepBacksUpDueRunningProjectsOnly(t *testing.T) {
	f := newHostBackupFixture(t)
	ctx := context.Background()
	paused := *f.inst
	paused.ProjectID, paused.Namespace, paused.Status = "p2", "excalibase-p2-mysql", string(domain.StatusPaused)
	off := *f.inst
	off.ProjectID, off.Namespace, off.BackupEnabled = "p3", "excalibase-p3-mysql", boolPtr(false)
	postgres := *f.inst
	postgres.ProjectID, postgres.DBType = "p4", domain.PostgreSQL
	for _, inst := range []*domain.DatabaseInstance{&paused, &off, &postgres} {
		if err := f.adapter.instances.Create(inst); err != nil {
			t.Fatal(err)
		}
	}
	sweep := NewHostMySQLBackupSweep(f.adapter, f.adapter.instances)
	sweep.Sweep(ctx)
	if runs := f.keys("backups/p1/mysql/runs/"); len(runs) != 2 {
		t.Fatalf("p1 runs %v", runs)
	}
	for _, project := range []string{"p2", "p3", "p4"} {
		if keys := f.keys("backups/" + project + "/"); len(keys) != 0 {
			t.Errorf("%s was backed up: %v", project, keys)
		}
	}
	f.tick(time.Hour)
	sweep.Sweep(ctx)
	if runs := f.keys("backups/p1/mysql/runs/"); len(runs) != 2 {
		t.Fatalf("a Free project was backed up again within 6 hours: %v", runs)
	}
	f.tick(5 * time.Hour)
	sweep.Sweep(ctx)
	if runs := f.keys("backups/p1/mysql/runs/"); len(runs) != 4 {
		t.Fatalf("a Free project is backed up after 6 hours: %v", runs)
	}
}
