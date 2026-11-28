package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/creditmeter"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/metrics"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

const (
	pruneBatch            = 5000
	postStepSampleTimeout = 30 * time.Second
)

// MeterNow is the database clock; every slot and every billed hour is on it.
func (s *Store) MeterNow(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := s.db.QueryRowContext(ctx, `SELECT now()`).Scan(&now)
	return now.UTC(), err
}

// MeterOrgs lists every org that holds a credit account, with when its credit
// began and when it was first sampled.
func (s *Store) MeterOrgs(ctx context.Context) ([]creditmeter.MeterOrg, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.org_id, a.created_at, (SELECT min(u.slot_start) FROM usage_samples u WHERE u.org_id = a.org_id)
		 FROM credit_accounts a ORDER BY a.org_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orgs []creditmeter.MeterOrg
	for rows.Next() {
		var org creditmeter.MeterOrg
		var first sql.NullTime
		if err := rows.Scan(&org.OrgID, &org.Since, &first); err != nil {
			return nil, err
		}
		org.Since = org.Since.UTC()
		if first.Valid {
			at := first.Time.UTC()
			org.FirstSample = &at
		}
		orgs = append(orgs, org)
	}
	return orgs, rows.Err()
}

// MarkMeteringEnabled records when metering first ran; later calls change nothing.
func (s *Store) MarkMeteringEnabled(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO credit_meter_state (key, at) VALUES ('enabled_at', now()) ON CONFLICT (key) DO NOTHING`)
	return err
}

// MeteringEnabledAt is when metering first ran; the zero time if it never has.
func (s *Store) MeteringEnabledAt(ctx context.Context) (time.Time, error) {
	var at time.Time
	err := s.db.QueryRowContext(ctx, `SELECT at FROM credit_meter_state WHERE key = 'enabled_at'`).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return at.UTC(), err
}

// UsageSlots are the samples taken in [from, to).
func (s *Store) UsageSlots(ctx context.Context, orgID string, from, to time.Time) ([]creditmeter.Slot, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT cpu_milli, memory_bytes, disk_bytes, allowance_cpu_milli, allowance_memory_bytes, allowance_disk_bytes FROM usage_samples
		 WHERE org_id = $1 AND slot_start >= $2 AND slot_start < $3 ORDER BY slot_start`, orgID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slots []creditmeter.Slot
	for rows.Next() {
		var slot creditmeter.Slot
		if err := rows.Scan(&slot.CPUMilli, &slot.MemoryBytes, &slot.DiskBytes,
			&slot.Allowance.CPUMilli, &slot.Allowance.MemoryBytes, &slot.Allowance.DiskBytes); err != nil {
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
	if err := s.sampleOrgUsage(ctx, tx, orgID); err != nil {
		return err
	}
	return tx.Commit()
}

// ObserveOrgUsage is the plan budget's observer: it samples the org inside the
// step's own transaction, before the step changes anything, so a service that
// is paused or resized still counts for the slot it ran in. A failure rolls
// back to a savepoint and is only logged: it must never refuse or abort the step.
func (s *Store) ObserveOrgUsage(ctx context.Context, tx planbudget.Querier, orgID string) {
	defer s.sampleAfterStep(orgID)
	if _, err := tx.ExecContext(ctx, `SAVEPOINT usage_sample`); err != nil {
		log.Printf("WARN: credit meter: sample before a step for %s: %v", orgID, err)
		return
	}
	if err := s.sampleOrgUsage(ctx, tx, orgID); err != nil {
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

// postStepTries and postStepRetryEvery bound how long a post-step sample waits for the step to commit.
var (
	postStepTries      = 20
	postStepRetryEvery = 250 * time.Millisecond
)

// postStepCap is how many post-step samples may wait at once: a tenth of the pool, at least one,
// so waiting samples can never use up the connections the platform needs.
func postStepCap(maxOpen int) int {
	if maxOpen <= 0 {
		return 2
	}
	return max(1, maxOpen/10)
}

func (s *Store) postStepSlots() chan struct{} {
	s.postStepOnce.Do(func() { s.postStep = make(chan struct{}, postStepCap(s.db.Stats().MaxOpenConnections)) })
	return s.postStep
}

// sampleAfterStep samples the org again once the step that is holding its lock has
// committed, so a service a step started counts for the slot too. It never waits
// on the lock while holding a connection: it tries the lock, gives the connection
// back, and tries again a moment later. If the step rolled back it just samples
// what is unchanged. When too many are waiting the next regular sample does it.
func (s *Store) sampleAfterStep(orgID string) {
	slots := s.postStepSlots()
	select {
	case slots <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-slots }()
		ctx, cancel := context.WithTimeout(context.Background(), postStepSampleTimeout)
		defer cancel()
		if err := s.sampleWhenFree(ctx, orgID); err != nil {
			log.Printf("WARN: credit meter: sample after a step for %s: %v", orgID, err)
		}
	}()
}

func (s *Store) sampleWhenFree(ctx context.Context, orgID string) error {
	for range postStepTries {
		sampled, err := s.trySampleOrg(ctx, orgID)
		if err != nil || sampled {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(postStepRetryEvery):
		}
	}
	log.Printf("WARN: credit meter: gave up sampling %s after a step: its lock was still held after %d tries; the next regular sample covers it", orgID, postStepTries)
	metrics.CountCreditMeterEvent(metrics.CreditMeterDroppedPostStepSample)
	return nil
}

// trySampleOrg samples the org if its lock is free right now; false means a step still holds it.
func (s *Store) trySampleOrg(ctx context.Context, orgID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin usage sample: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	var free bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1, hashtext($2))`, planbudget.OrgLockSpace, orgID).Scan(&free); err != nil {
		return false, fmt.Errorf("try the org lock for a usage sample: %w", err)
	}
	if !free {
		return false, nil
	}
	if err := s.sampleOrgUsage(ctx, tx, orgID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// SetCreditAllowance sets how a plan's free allowance is looked up when an org is
// sampled; ok is false for a plan the meter does not charge. Set once at boot.
func (s *Store) SetCreditAllowance(lookup func(tier string) (creditmeter.Allowance, bool)) {
	s.allowanceFor = lookup
}

// sampleOrgUsage writes the org's reservation into the slot the database clock
// is in. A second sample in the slot keeps the larger of each dimension, so a
// service that ran at any point of the slot counts for all of it. The caller
// holds the org lock.
func (s *Store) sampleOrgUsage(ctx context.Context, q planbudget.Querier, orgID string) error {
	var metered bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM credit_accounts WHERE org_id = $1)`, orgID).Scan(&metered); err != nil {
		return fmt.Errorf("check the org is metered: %w", err)
	}
	if !metered {
		return nil
	}
	allowance, charged, err := s.orgAllowance(ctx, q, orgID)
	if err != nil {
		return err
	}
	var total planbudget.Size
	var services []planbudget.ServiceUse
	var unrecorded int
	if charged {
		if services, unrecorded, err = billableServices(ctx, q, orgID); err != nil {
			return err
		}
		for _, service := range services {
			total = total.Add(service.Size)
		}
	}
	breakdown, err := json.Marshal(services)
	if err != nil {
		return fmt.Errorf("write the sample breakdown: %w", err)
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO usage_samples (org_id, slot_start, cpu_milli, memory_bytes, disk_bytes, breakdown,
		                           allowance_cpu_milli, allowance_memory_bytes, allowance_disk_bytes, unrecorded_services)
		VALUES ($1, date_bin('5 minutes', now(), TIMESTAMPTZ '2000-01-01'), $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (org_id, slot_start) DO UPDATE SET
			breakdown    = CASE WHEN EXCLUDED.cpu_milli >= usage_samples.cpu_milli THEN EXCLUDED.breakdown ELSE usage_samples.breakdown END,
			cpu_milli    = GREATEST(usage_samples.cpu_milli, EXCLUDED.cpu_milli),
			memory_bytes = GREATEST(usage_samples.memory_bytes, EXCLUDED.memory_bytes),
			disk_bytes   = GREATEST(usage_samples.disk_bytes, EXCLUDED.disk_bytes),
			allowance_cpu_milli    = GREATEST(usage_samples.allowance_cpu_milli, EXCLUDED.allowance_cpu_milli),
			allowance_memory_bytes = GREATEST(usage_samples.allowance_memory_bytes, EXCLUDED.allowance_memory_bytes),
			allowance_disk_bytes   = GREATEST(usage_samples.allowance_disk_bytes, EXCLUDED.allowance_disk_bytes),
			unrecorded_services    = GREATEST(usage_samples.unrecorded_services, EXCLUDED.unrecorded_services),
			sampled_at   = now()`,
		orgID, total.CPUMilli, total.MemoryBytes, total.DiskBytes, breakdown,
		allowance.CPUMilli, allowance.MemoryBytes, allowance.DiskBytes, unrecorded)
	if err != nil {
		return fmt.Errorf("write the usage sample: %w", err)
	}
	return nil
}

// orgAllowance is the allowance of the org's plan right now. A plan the meter does
// not charge (charged is false) is counted and sampled at zero, so its lots still
// reach their expiry. An org that no longer exists has no services and no plan.
func (s *Store) orgAllowance(ctx context.Context, q planbudget.Querier, orgID string) (creditmeter.Allowance, bool, error) {
	var tier string
	err := q.QueryRowContext(ctx, `SELECT tier FROM orgs WHERE id = $1`, orgID).Scan(&tier)
	if errors.Is(err, sql.ErrNoRows) {
		return creditmeter.Allowance{}, false, nil
	}
	if err != nil {
		return creditmeter.Allowance{}, false, fmt.Errorf("read the org plan: %w", err)
	}
	if s.allowanceFor == nil {
		return creditmeter.Allowance{}, true, nil
	}
	allowance, charged := s.allowanceFor(tier)
	if !charged {
		log.Printf("credit meter: org %s is on plan %q, which is not metered", orgID, tier)
		metrics.CountCreditMeterEvent(metrics.CreditMeterUnmeteredPlan)
	}
	return allowance, charged, nil
}

// billableServices lists what the org's services reserve. A database whose size
// was never recorded is billed at its plan's default size, not skipped, and counted.
func billableServices(ctx context.Context, q planbudget.Querier, orgID string) ([]planbudget.ServiceUse, int, error) {
	services, err := planbudget.OrgServices(ctx, q, orgID)
	if err != nil {
		return nil, 0, err
	}
	unrecorded := 0
	for i, service := range services {
		if !service.Unrecorded {
			continue
		}
		size, err := unrecordedDefault(ctx, q, service.ProjectID)
		if err != nil {
			return nil, 0, fmt.Errorf("default size of %s: %w", service.ProjectID, err)
		}
		services[i].Size = size
		unrecorded++
		log.Printf("WARN: credit meter: %s has no recorded size and is billed at its plan's default", service.ProjectID)
		metrics.CountCreditMeterEvent(metrics.CreditMeterUnrecordedSize)
	}
	return services, unrecorded, nil
}

// unrecordedDefault is what a database would reserve at its plan's default size.
func unrecordedDefault(ctx context.Context, q planbudget.Querier, projectID string) (planbudget.Size, error) {
	var tier, status, storageSize string
	err := q.QueryRowContext(ctx, `SELECT tier, status, COALESCE(storage_size, '') FROM database_instances WHERE project_id = $1`, projectID).Scan(&tier, &status, &storageSize)
	if err != nil {
		return planbudget.Size{}, err
	}
	perCopy, copies, err := tierReservation(ctx, q, domain.TierType(tier), storageSize)
	if err != nil {
		return planbudget.Size{}, err
	}
	return planbudget.ReservedDatabase(planbudget.Database{Status: status, PerCopy: perCopy, Copies: copies}), nil
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
