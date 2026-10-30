//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-658: a failed send acks against its lease, whose end is a nanosecond
// timestamp; recording it must not be refused as out of range for integer.
func TestLogDrains_FailOfALeasedBatchAtRealTimestamps(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	drain := newTestDrain("ld_leased", "pa")
	createDrains(t, store, drain)
	leaseTo := drain.CursorNS + int64(30*time.Second)
	leased, err := store.LeaseLogDrain(ctx, domain.LogDrainLease{ID: drain.ID, From: drain.CursorNS, To: leaseTo, Lines: 3,
		Until: drainT0.Add(time.Minute), Now: drainT0})
	if err != nil || !leased {
		t.Fatalf("lease = %v, %v", leased, err)
	}
	failure := domain.LogDrainFailure{ID: drain.ID, From: drain.CursorNS, LeaseTo: leaseTo,
		Error: "the destination is not a public address", Next: drainT0.Add(30 * time.Second)}
	if ok, err := store.FailLogDrain(ctx, failure); err != nil || !ok {
		t.Fatalf("fail of the leased batch = %v, %v", ok, err)
	}
	failed := findDrain(t, store, drain.ID)
	if failed.ConsecutiveFailures != 1 || failed.LastError == "" || failed.LeaseTo != 0 || failed.Status != domain.LogDrainFailing {
		t.Errorf("after the failure %+v", failed)
	}
}
