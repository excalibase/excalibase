package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/creditmeter"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

const pruneBatch = 5000

// MeterNow is the database clock; every slot and every billed hour is on it.
func (s *Store) MeterNow(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := s.db.QueryRowContext(ctx, `SELECT now()`).Scan(&now)
	return now.UTC(), err
}

// MeterOrgs lists every org that holds a credit account. An org deleted since
// keeps its ledger and is metered as a free org with nothing reserved, so its
// lots still reach their expiry.
func (s *Store) MeterOrgs(ctx context.Context) ([]creditmeter.MeterOrg, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.org_id, COALESCE(o.tier, 'FREE'), a.balance_micros, a.created_at
		 FROM credit_accounts a LEFT JOIN orgs o ON o.id = a.org_id ORDER BY a.org_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orgs []creditmeter.MeterOrg
	for rows.Next() {
		var org creditmeter.MeterOrg
		var balance int64
		if err := rows.Scan(&org.OrgID, &org.Tier, &balance, &org.Since); err != nil {
			return nil, err
		}
		org.Balance, org.Since = credits.Micros(balance), org.Since.UTC()
		orgs = append(orgs, org)
	}
	return orgs, rows.Err()
}

// UsageSlots are the samples taken in [from, to).
func (s *Store) UsageSlots(ctx context.Context, orgID string, from, to time.Time) ([]creditmeter.Slot, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT cpu_milli, memory_bytes, disk_bytes FROM usage_samples
		 WHERE org_id = $1 AND slot_start >= $2 AND slot_start < $3 ORDER BY slot_start`, orgID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slots []creditmeter.Slot
	for rows.Next() {
		var slot creditmeter.Slot
		if err := rows.Scan(&slot.CPUMilli, &slot.MemoryBytes, &slot.DiskBytes); err != nil {
			return nil, err
		}
		slots = append(slots, slot)
	}
	return slots, rows.Err()
}

// SampledOrgs are the orgs that are metered: the ones that hold a credit account.
func (s *Store) SampledOrgs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT org_id FROM credit_accounts ORDER BY org_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orgs []string
	for rows.Next() {
		var orgID string
		if err := rows.Scan(&orgID); err != nil {
			return nil, err
		}
		orgs = append(orgs, orgID)
	}
	return orgs, rows.Err()
}

// SampleOrg records what the org reserves now. It takes the org lock, so it
// never sees a half-applied admission.
func (s *Store) SampleOrg(ctx context.Context, orgID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin usage sample: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, planbudget.OrgLockSpace, orgID); err != nil {
		return fmt.Errorf("lock org for a usage sample: %w", err)
	}
	if err := sampleOrgUsage(ctx, tx, orgID); err != nil {
		return err
	}
	return tx.Commit()
}

// ObserveOrgUsage is the plan budget's observer: it samples the org inside the
// step's own transaction, before the step changes anything, so a service that
// is paused or resized still counts for the slot it ran in. A failure rolls
// back to a savepoint and is only logged: it must never refuse or abort the step.
func (s *Store) ObserveOrgUsage(ctx context.Context, tx planbudget.Querier, orgID string) {
	if _, err := tx.ExecContext(ctx, `SAVEPOINT usage_sample`); err != nil {
		log.Printf("WARN: credit meter: sample before a step for %s: %v", orgID, err)
		return
	}
	if err := sampleOrgUsage(ctx, tx, orgID); err != nil {
		log.Printf("WARN: credit meter: sample before a step for %s: %v", orgID, err)
		if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT usage_sample`); rollbackErr != nil {
			log.Printf("WARN: credit meter: roll back the sample for %s: %v", orgID, rollbackErr)
		}
		return
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT usage_sample`); err != nil {
		log.Printf("WARN: credit meter: release the sample for %s: %v", orgID, err)
	}
}

// sampleOrgUsage writes the org's reservation into the slot the database clock
// is in. A second sample in the slot keeps the larger of each dimension, so a
// service that ran at any point of the slot counts for all of it. The caller
// holds the org lock.
func sampleOrgUsage(ctx context.Context, q planbudget.Querier, orgID string) error {
	var metered bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM credit_accounts WHERE org_id = $1)`, orgID).Scan(&metered); err != nil {
		return fmt.Errorf("check the org is metered: %w", err)
	}
	if !metered {
		return nil
	}
	services, err := planbudget.OrgServices(ctx, q, orgID)
	if err != nil {
		return err
	}
	var total planbudget.Size
	for _, service := range services {
		if service.Unrecorded {
			return fmt.Errorf("%w (project %s)", planbudget.ErrUnrecordedSize, service.ProjectID)
		}
		total = total.Add(service.Size)
	}
	breakdown, err := json.Marshal(services)
	if err != nil {
		return fmt.Errorf("write the sample breakdown: %w", err)
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO usage_samples (org_id, slot_start, cpu_milli, memory_bytes, disk_bytes, breakdown)
		VALUES ($1, date_bin('5 minutes', now(), TIMESTAMPTZ '2000-01-01'), $2, $3, $4, $5)
		ON CONFLICT (org_id, slot_start) DO UPDATE SET
			breakdown    = CASE WHEN EXCLUDED.cpu_milli >= usage_samples.cpu_milli THEN EXCLUDED.breakdown ELSE usage_samples.breakdown END,
			cpu_milli    = GREATEST(usage_samples.cpu_milli, EXCLUDED.cpu_milli),
			memory_bytes = GREATEST(usage_samples.memory_bytes, EXCLUDED.memory_bytes),
			disk_bytes   = GREATEST(usage_samples.disk_bytes, EXCLUDED.disk_bytes),
			sampled_at   = now()`,
		orgID, total.CPUMilli, total.MemoryBytes, total.DiskBytes, breakdown)
	if err != nil {
		return fmt.Errorf("write the usage sample: %w", err)
	}
	return nil
}

// PruneUsageSamples deletes samples older than keep that the org's watermark
// has already passed. A sample not yet billed is the only record of what the
// org owes and is never deleted.
func (s *Store) PruneUsageSamples(ctx context.Context, keep time.Duration) (int64, error) {
	var removed int64
	for {
		result, err := s.db.ExecContext(ctx, `
			DELETE FROM usage_samples WHERE ctid IN (
				SELECT u.ctid FROM usage_samples u JOIN credit_meter_watermarks w ON w.org_id = u.org_id
				WHERE u.slot_start < w.watermark AND u.slot_start < now() - make_interval(secs => $1)
				LIMIT $2)`, keep.Seconds(), pruneBatch)
		if err != nil {
			return removed, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return removed, err
		}
		removed += affected
		if affected < pruneBatch {
			return removed, nil
		}
	}
}

var (
	_ creditmeter.Store       = (*Store)(nil)
	_ creditmeter.SampleStore = (*Store)(nil)
)

// CreditUsageBurn is what usage drew on each UTC day since `from`, and when the first of it began.
func (s *Store) CreditUsageBurn(ctx context.Context, orgID string, from time.Time) (credits.UsageBurn, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT (period_start AT TIME ZONE 'UTC')::date, -sum(amount_micros), min(period_start)
		 FROM credit_entries WHERE org_id = $1 AND kind = 'usage' AND period_start >= $2
		 GROUP BY 1 ORDER BY 1`, orgID, from.UTC())
	if err != nil {
		return credits.UsageBurn{}, err
	}
	defer rows.Close()
	var burn credits.UsageBurn
	for rows.Next() {
		var day, first time.Time
		var micros int64
		if err := rows.Scan(&day, &micros, &first); err != nil {
			return credits.UsageBurn{}, err
		}
		burn.Days = append(burn.Days, credits.DayBurn{Day: time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC), Micros: credits.Micros(micros)})
		if burn.Since == nil || first.Before(*burn.Since) {
			first = first.UTC()
			burn.Since = &first
		}
	}
	return burn, rows.Err()
}
