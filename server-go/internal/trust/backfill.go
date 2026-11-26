package trust

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// BackfillRun records that the grandfather backfill finished, and the cutoff it used.
type BackfillRun struct {
	FinishedAt time.Time
	Cutoff     time.Time
}

// BackfillMarkers reads the backfill marker; the Postgres store implements it.
type BackfillMarkers interface {
	// LatestBackfill returns nil, nil when no backfill has finished.
	LatestBackfill(ctx context.Context) (*BackfillRun, error)
}

// ErrBackfillMissing refuses a start with the trial flag on and no backfill.
var ErrBackfillMissing = errors.New("FEATURE_LIMITEDTRIAL is on but the grandfather backfill has not run: " +
	"with the flag on, an account with no trust decision is limited, so every existing account would be limited. " +
	"Run the backfill (it records a marker) before turning the flag on")

// ErrBackfillCutoffMismatch refuses a start whose recorded backfill used another cutoff than the configured one.
var ErrBackfillCutoffMismatch = errors.New("the recorded grandfather backfill used a different cutoff than TRUST_GRANDFATHER_BEFORE")

const cutoffLayout = "2006-01-02"

// RequireBackfill is the boot guard: with the flag on it demands a recorded
// backfill that used the configured cutoff. Flag off, it reads nothing. A
// marker that cannot be read refuses too, since an unknown answer must not
// switch the trial on.
func RequireBackfill(ctx context.Context, flags features.Flags, markers BackfillMarkers, cutoff time.Time) error {
	if !features.Enabled(ctx, flags, features.LimitedTrial) {
		return nil
	}
	if markers == nil {
		return fmt.Errorf("%w (this platform store cannot record one)", ErrBackfillMissing)
	}
	run, err := markers.LatestBackfill(ctx)
	if err != nil {
		return fmt.Errorf("trust: read the backfill marker: %w", err)
	}
	if run == nil {
		return ErrBackfillMissing
	}
	if !run.Cutoff.Equal(cutoff) {
		return fmt.Errorf("%w: recorded %s, configured %s; run the backfill again with the configured cutoff or set TRUST_GRANDFATHER_BEFORE back",
			ErrBackfillCutoffMismatch, run.Cutoff.UTC().Format(cutoffLayout), cutoff.UTC().Format(cutoffLayout))
	}
	return nil
}
