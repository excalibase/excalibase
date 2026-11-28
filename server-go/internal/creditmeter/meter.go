package creditmeter

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/credits"
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
	OrgID   string
	Tier    string
	Balance credits.Micros
	// Since is when the org's credit began; usage before it is never billed.
	Since time.Time
}

// Store is what the meter reads. Every time in it is the database clock.
type Store interface {
	MeterNow(ctx context.Context) (time.Time, error)
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
	store     Store
	ledger    Ledger
	prices    Prices
	allowance func(tier string) Allowance
}

func NewMeter(store Store, ledger Ledger, prices Prices, allowance func(tier string) Allowance) *Meter {
	return &Meter{store: store, ledger: ledger, prices: prices, allowance: allowance}
}

// Run bills every hour that has settled since each org's watermark. A failure
// for one org is reported at the end and does not stop the others.
func (m *Meter) Run(ctx context.Context) error {
	now, err := m.store.MeterNow(ctx)
	if err != nil {
		return fmt.Errorf("read the database clock: %w", err)
	}
	orgs, err := m.store.MeterOrgs(ctx)
	if err != nil {
		return fmt.Errorf("list metered orgs: %w", err)
	}
	var failures []error
	for _, org := range orgs {
		if err := m.meterOrg(ctx, org, now); err != nil {
			failures = append(failures, fmt.Errorf("org %s: %w", org.OrgID, err))
		}
	}
	return errors.Join(failures...)
}

func (m *Meter) meterOrg(ctx context.Context, org MeterOrg, now time.Time) error {
	hour, err := m.firstUnbilledHour(ctx, org)
	if err != nil {
		return err
	}
	allowance := m.allowance(org.Tier)
	exhausted := org.Balance <= 0
	for billed := 0; billed < maxHoursPerPass && !hour.Add(time.Hour+settle).After(now); billed++ {
		if !exhausted {
			ranOut, err := m.billHour(ctx, org.OrgID, hour, allowance)
			if err != nil {
				return err
			}
			exhausted = ranOut
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

func (m *Meter) firstUnbilledHour(ctx context.Context, org MeterOrg) (time.Time, error) {
	mark, started, err := m.ledger.MeterWatermark(ctx, org.OrgID)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the watermark: %w", err)
	}
	if started {
		return mark.UTC(), nil
	}
	return ceilHour(org.Since.UTC()), nil
}

func ceilHour(at time.Time) time.Time {
	floor := at.Truncate(time.Hour)
	if floor.Equal(at) {
		return at
	}
	return floor.Add(time.Hour)
}

// billHour debits one hour. ranOut says the credit could not pay for all of it.
func (m *Meter) billHour(ctx context.Context, orgID string, hour time.Time, allowance Allowance) (ranOut bool, err error) {
	end := hour.Add(time.Hour)
	slots, err := m.store.UsageSlots(ctx, orgID, hour, end)
	if err != nil {
		return false, fmt.Errorf("read the samples of %s: %w", hour.Format(time.RFC3339), err)
	}
	used := SumOver(slots, allowance)
	cost := used.Cost(m.prices)
	if cost <= 0 {
		return false, nil
	}
	result, err := m.ledger.Debit(ctx, credits.DebitRequest{
		OrgID: orgID, Amount: cost, PeriodStart: hour, PeriodEnd: end,
		IdempotencyKey: fmt.Sprintf("usage:%s:%s", orgID, hour.Format(time.RFC3339)),
		Details: map[string]any{
			"priceVersion": m.prices.Version, "slots": len(slots),
			"cpuMilliSlots": used.CPUMilliSlots, "memoryByteSlots": used.MemoryByteSlots, "diskByteSlots": used.DiskByteSlots,
		},
	})
	if err != nil {
		return false, fmt.Errorf("debit %s: %w", hour.Format(time.RFC3339), err)
	}
	if result.Uncovered > 0 {
		// TODO(EXC-866 C.5): the $0 rules. The ledger takes at most what the lots hold and records the
		// rest as uncovered; here the pass only stops debiting. Alerts, the refusal of growth, the
		// grace period and pausing compute above the allowance are a separate step.
		log.Printf("credit meter: org %s ran out of credit in the hour of %s (%d micros uncovered)", orgID, hour.Format(time.RFC3339), result.Uncovered)
		return true, nil
	}
	return false, nil
}
