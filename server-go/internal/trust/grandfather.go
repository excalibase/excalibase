package trust

import (
	"context"
	"fmt"
	"time"
)

// Grandfatherer makes the accounts that existed before the trial full, and records that it did.
type Grandfatherer interface {
	// GrandfatherUsersBefore gives every account created before cutoff and
	// holding no decision a full one, and says how many it made.
	GrandfatherUsersBefore(ctx context.Context, cutoff time.Time) (int, error)
	RecordBackfill(ctx context.Context, run BackfillRun) error
}

// RunBackfill is the one-off the operator runs before switching the trial on.
// An account created on or after cutoff is not grandfathered and must verify.
// The marker is written only after the work succeeded, so a failed run leaves
// the boot guard closed; a repeat is harmless.
func RunBackfill(ctx context.Context, store Grandfatherer, cutoff, now time.Time) (int, error) {
	if cutoff.IsZero() || cutoff.After(now) {
		return 0, fmt.Errorf("%w: the cutoff must be a past time, got %s", ErrInvalidRecord, cutoff.UTC().Format(time.RFC3339))
	}
	granted, err := store.GrandfatherUsersBefore(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("trust backfill: %w", err)
	}
	if err := store.RecordBackfill(ctx, BackfillRun{FinishedAt: now, Cutoff: cutoff}); err != nil {
		return granted, err
	}
	return granted, nil
}
