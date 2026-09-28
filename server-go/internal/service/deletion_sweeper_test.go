package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

type countingSweep struct{ deletions, purges int }

func (c *countingSweep) RunDueDeletions(context.Context) DeletionSweepReport {
	c.deletions++
	return DeletionSweepReport{Deleted: []string{"p"}}
}

func (c *countingSweep) PurgeDueRetainedBackups(context.Context) RetainedBackupSweepReport {
	c.purges++
	return RetainedBackupSweepReport{}
}

type neverLeader struct{}

func (neverLeader) Acquire(context.Context) (storage.LeaderLease, bool, error) {
	return nil, false, nil
}

func TestDeletionSweeperRunsBothSweepsWhenLeading(t *testing.T) {
	work := &countingSweep{}
	sweeper := NewDeletionSweeper(DeletionSweeperConfig{Work: work, Lock: AlwaysLeader{}})
	sweeper.tick(context.Background())
	if work.deletions != 1 || work.purges != 1 {
		t.Fatalf("deletions=%d purges=%d, want one of each", work.deletions, work.purges)
	}
}

func TestDeletionSweeperDoesNothingWithoutLeadership(t *testing.T) {
	work := &countingSweep{}
	sweeper := NewDeletionSweeper(DeletionSweeperConfig{Work: work, Lock: neverLeader{}})
	sweeper.tick(context.Background())
	if work.deletions+work.purges != 0 {
		t.Fatal("only the leading replica sweeps")
	}
}

func TestDeletionSweeperStartStop(t *testing.T) {
	sweeper := NewDeletionSweeper(DeletionSweeperConfig{Work: &countingSweep{}, Lock: AlwaysLeader{}})
	sweeper.Start(context.Background())
	sweeper.Start(context.Background())
	sweeper.Stop()
	sweeper.Stop()
}
