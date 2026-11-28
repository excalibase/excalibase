package creditmeter

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/metrics"
)

const (
	// settle is how long after an hour ends it waits to be billed, so late samples land first.
	settle = 10 * time.Minute
	// maxHoursPerPass bounds one org's catch-up after downtime to a week, so a long outage
	// is worked off over several passes instead of one very long one.
	maxHoursPerPass = 24 * 7
)

// MeterOrg is an org that holds a credit account, and where its billing starts.
type MeterOrg struct {
	OrgID string
	// Since is when the org's credit began; usage before it is never billed.
	Since time.Time
	// FirstSample is when the org was first sampled; nil until it has been.
	FirstSample *time.Time
}

// Store is what the meter reads. Every time in it is the database clock.
type Store interface {
	MeterNow(ctx context.Context) (time.Time, error)
	// MeteringEnabledAt is when metering first ran; nothing before it is billed.
	MeteringEnabledAt(ctx context.Context) (time.Time, error)
	MeterOrgs(ctx context.Context) ([]MeterOrg, error)
	// UsageSlots are the samples taken in [from, to).
	UsageSlots(ctx context.Context, orgID string, from, to time.Time) ([]Slot, error)
}

// Ledger is the part of the credit service the meter bills through.
type Ledger interface {
	Debit(ctx context.Context, request credits.DebitRequest) (credits.DebitResult, error)
	MeterWatermark(ctx context.Context, orgID string) (time.Time, bool, error)
	AdvanceMeterWatermark(ctx context.Context, orgID string, to time.Time) error
}

// Meter bills each org's sampled usage to its credit, an hour at a time.
type Meter struct {
	store  Store
	ledger Ledger
	prices Prices
}

func NewMeter(store Store, ledger Ledger, prices Prices) *Meter {
	return &Meter{store: store, ledger: ledger, prices: prices}
}

// Run bills every hour that has settled since each org's watermark. A failure
// for one org is reported at the end and does not stop the others.
func (m *Meter) Run(ctx context.Context) error {
	now, err := m.store.MeterNow(ctx)
	if err != nil {
		return fmt.Errorf("read the database clock: %w", err)
	}
	enabledAt, err := m.store.MeteringEnabledAt(ctx)
	if err != nil {
		return fmt.Errorf("read when metering was enabled: %w", err)
	}
	orgs, err := m.store.MeterOrgs(ctx)
	if err != nil {
		return fmt.Errorf("list metered orgs: %w", err)
	}
	var failures []error
	for _, org := range orgs {
		if err := m.meterOrg(ctx, org, enabledAt, now); err != nil {
			failures = append(failures, fmt.Errorf("org %s: %w", org.OrgID, err))
		}
	}
	return errors.Join(failures...)
}

func (m *Meter) meterOrg(ctx context.Context, org MeterOrg, enabledAt, now time.Time) error {
	hour, ready, err := m.firstUnbilledHour(ctx, org, enabledAt)
	if err != nil || !ready {
		return err
	}
	for billed := 0; billed < maxHoursPerPass && !hour.Add(time.Hour+settle).After(now); billed++ {
		if err := m.billHour(ctx, org.OrgID, hour); err != nil {
			return err
		}
		// The watermark moves past hours that bill nothing too, so credit added
		// later never pays for them and a lot's expiry is never held back.
		if err := m.ledger.AdvanceMeterWatermark(ctx, org.OrgID, hour.Add(time.Hour)); err != nil {
			return fmt.Errorf("advance the watermark: %w", err)
		}
		hour = hour.Add(time.Hour)
	}
	return nil
}

// firstUnbilledHour is where the watermark stands, or for an org never metered the
// first whole hour after the latest of its credit, the enabling of metering and
// its first sample, so no empty hours before any of them are walked. An org with
// no sample yet is not ready.
func (m *Meter) firstUnbilledHour(ctx context.Context, org MeterOrg, enabledAt time.Time) (time.Time, bool, error) {
	mark, started, err := m.ledger.MeterWatermark(ctx, org.OrgID)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read the watermark: %w", err)
	}
	if started {
		return mark.UTC(), true, nil
	}
	if org.FirstSample == nil {
		return time.Time{}, false, nil
	}
	latest := org.Since
	for _, at := range []time.Time{enabledAt, *org.FirstSample} {
		if at.After(latest) {
			latest = at
		}
	}
	return ceilHour(latest.UTC()), true, nil
}

func ceilHour(at time.Time) time.Time {
	floor := at.Truncate(time.Hour)
	if floor.Equal(at) {
		return at
	}
	return floor.Add(time.Hour)
}

// billHour debits one hour. The ledger takes what the lots hold and records the
// rest on the entry as uncovered, so every hour the credit could not pay stays
// visible, and an hour is decided by the balance when it is billed: credit added
// later never pays for it.
func (m *Meter) billHour(ctx context.Context, orgID string, hour time.Time) error {
	end := hour.Add(time.Hour)
	slots, err := m.store.UsageSlots(ctx, orgID, hour, end)
	if err != nil {
		return fmt.Errorf("read the samples of %s: %w", hour.Format(time.RFC3339), err)
	}
	used := SumOver(slots)
	cost := used.Cost(m.prices)
	if cost <= 0 {
		return nil
	}
	result, err := m.ledger.Debit(ctx, credits.DebitRequest{
		OrgID: orgID, Amount: cost, PeriodStart: hour, PeriodEnd: end,
		IdempotencyKey: fmt.Sprintf("usage:%s:%s", orgID, hour.Format(time.RFC3339)),
		Details: map[string]any{
			"priceVersion": m.prices.Version, "slots": len(slots),
			"cpuMilliSlots": used.CPUMilliSlots, "memoryByteSlots": used.MemoryByteSlots, "diskByteSlots": used.DiskByteSlots,
		},
	})
	if errors.Is(err, credits.ErrIdempotencyConflict) {
		// A crash after the debit and before the watermark, then a change of price,
		// makes the retry come out differently. The hour is on the ledger: it is billed.
		log.Printf("credit meter: org %s hour %s is already billed at another amount than %d micros; keeping what was billed", orgID, hour.Format(time.RFC3339), cost)
		metrics.CountCreditMeterEvent(metrics.CreditMeterRepricedHour)
		return nil
	}
	if err != nil {
		return fmt.Errorf("debit %s: %w", hour.Format(time.RFC3339), err)
	}
	if result.Uncovered > 0 {
		// The shortfall is written off (recorded, never collected). What an org at $0 may still run is the
		// $0 rules' business (creditzero and the plan budget gate), not the meter's.
		metrics.CountCreditMeterEvent(metrics.CreditMeterShortfallHour)
	}
	return nil
}
