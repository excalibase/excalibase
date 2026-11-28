//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/excalibase/provisioning-poc/internal/creditmeter"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/metrics"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

func databaseNow(t *testing.T, store *Store) time.Time {
	t.Helper()
	now, err := store.MeterNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// fundAt gives an org one lot as of `at`, so a test can place credit in the database's past.
func fundAt(t *testing.T, store *Store, orgID, key string, amount credits.Micros, expiresAt *time.Time, at time.Time) {
	t.Helper()
	_, err := store.AdjustCredit(context.Background(), credits.Adjustment{
		OrgID: orgID, Amount: amount, Actor: "admin-1", Reason: "seed credit for a test", IdempotencyKey: key, ExpiresAt: expiresAt,
		Audit: credits.AuditRecord{UserID: "admin-1", Action: "admin.credit_adjust", Resource: "org", Details: "seed"},
	}, at)
	if err != nil {
		t.Fatalf("fund %s: %v", orgID, err)
	}
}

type sampleRow struct {
	slot   time.Time
	cpu    int64
	memory int64
	disk   int64
}

func samplesOf(t *testing.T, store *Store, orgID string) []sampleRow {
	t.Helper()
	rows, err := store.db.Query(`SELECT slot_start, cpu_milli, memory_bytes, disk_bytes FROM usage_samples WHERE org_id = $1 ORDER BY slot_start`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []sampleRow
	for rows.Next() {
		var row sampleRow
		if err := rows.Scan(&row.slot, &row.cpu, &row.memory, &row.disk); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}

// putSample writes a sample at an exact slot, as the sampler would have.
func putSample(t *testing.T, store *Store, orgID string, slot time.Time, cpuMilli int64) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO usage_samples (org_id, slot_start, cpu_milli, memory_bytes, disk_bytes) VALUES ($1, $2, $3, 0, 0)`, orgID, slot.UTC(), cpuMilli); err != nil {
		t.Fatal(err)
	}
}

func TestASampleRecordsWhatTheOrgReservesAndKeepsTheLargestOfASlot(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "sm")
	fundOrg(t, store, org, "seed-sm", credits.FromDollars(5), nil)
	newDatabase(t, store, "proj-sm-db", org)
	ctx := context.Background()

	for range 2 {
		if err := store.SampleOrg(ctx, org); err != nil {
			t.Fatal(err)
		}
	}
	rows := samplesOf(t, store, org)
	if len(rows) != 1 {
		t.Fatalf("two samples in one slot are one row, got %d", len(rows))
	}
	if rows[0].cpu != freeDatabaseCPU || rows[0].disk != freeDatabaseDisk {
		t.Fatalf("a free database holds 500m and 5 GiB, got %+v", rows[0])
	}
	if !rows[0].slot.Equal(rows[0].slot.Truncate(5 * time.Minute)) {
		t.Fatalf("a slot starts on a five-minute boundary, got %v", rows[0].slot)
	}

	// The database pauses and the slot is sampled again: the slot keeps the CPU it ran with.
	if _, err := store.db.Exec(`UPDATE database_instances SET status = 'PAUSED' WHERE project_id = 'proj-sm-db'`); err != nil {
		t.Fatal(err)
	}
	if err := store.SampleOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	rows = samplesOf(t, store, org)
	if len(rows) != 1 || rows[0].cpu != freeDatabaseCPU {
		t.Fatalf("a running service counts for the whole slot, got %+v", rows)
	}
}

func TestAnOrgWithNoCreditAccountIsNotSampled(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "nc")
	newDatabase(t, store, "proj-nc-db", org)
	if err := store.SampleOrg(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	if rows := samplesOf(t, store, org); len(rows) != 0 {
		t.Fatalf("an org that never held credit is not metered, got %+v", rows)
	}
}

func TestADatabaseWhoseSizeIsUnknownIsSampledAtItsPlansDefaultSize(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "uk")
	fundOrg(t, store, org, "seed-uk", credits.FromDollars(5), nil)
	newDatabase(t, store, "proj-uk-db", org)
	if _, err := store.db.Exec(`UPDATE database_instances SET reserved_copies = 0, reserved_cpu_milli = 0, reserved_memory_bytes = 0, reserved_disk_bytes = 0 WHERE project_id = 'proj-uk-db'`); err != nil {
		t.Fatal(err)
	}
	if err := store.SampleOrg(context.Background(), org); err != nil {
		t.Fatalf("an unreadable size must be billed at the default, not refused: %v", err)
	}
	rows := samplesOf(t, store, org)
	if len(rows) != 1 || rows[0].cpu != freeDatabaseCPU || rows[0].disk != freeDatabaseDisk {
		t.Fatalf("a free database defaults to 500m and 5 GiB, got %+v", rows)
	}
	var unrecorded int
	if err := store.db.QueryRow(`SELECT unrecorded_services FROM usage_samples WHERE org_id = $1`, org).Scan(&unrecorded); err != nil || unrecorded != 1 {
		t.Fatalf("the sample says one service was billed at a default: %d %v", unrecorded, err)
	}
}

func TestTheSampleKeepsTheAllowanceOfThePlanItWasTakenUnder(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "al")
	fundOrg(t, store, org, "seed-al", credits.FromDollars(5), nil)
	store.SetCreditAllowance(testAllowance)
	if err := store.SampleOrg(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	var cpu, memory, disk int64
	if err := store.db.QueryRow(`SELECT allowance_cpu_milli, allowance_memory_bytes, allowance_disk_bytes FROM usage_samples WHERE org_id = $1`, org).Scan(&cpu, &memory, &disk); err != nil {
		t.Fatal(err)
	}
	if cpu != 1000 || memory != 1<<30 || disk != 10<<30 {
		t.Fatalf("a free org's sample carries the free allowance, got %d %d %d", cpu, memory, disk)
	}
	if _, err := store.db.Exec(`UPDATE orgs SET tier = 'STANDARD' WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if err := store.SampleOrg(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT allowance_cpu_milli FROM usage_samples WHERE org_id = $1`, org).Scan(&cpu); err != nil || cpu != 1000 {
		t.Fatalf("in the same slot the larger allowance stays (it errs toward the user): %d %v", cpu, err)
	}
}

func TestAPlanTheMeterDoesNotChargeIsSampledAsZeroAndCounted(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "en")
	fundOrg(t, store, org, "seed-en", credits.FromDollars(5), nil)
	newDatabase(t, store, "proj-en-db", org)
	store.SetCreditAllowance(testAllowance)
	if _, err := store.db.Exec(`UPDATE orgs SET tier = 'ENTERPRISE' WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if err := store.SampleOrg(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	rows := samplesOf(t, store, org)
	if len(rows) != 1 || rows[0].cpu != 0 || rows[0].disk != 0 {
		t.Fatalf("an org on an unmetered plan is never charged, but is sampled at zero so its watermark and lots move: %+v", rows)
	}
}

func TestASampleWaitsWhileAStepHoldsTheOrgLock(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "lk")
	fundOrg(t, store, org, "seed-lk", credits.FromDollars(5), nil)
	newDatabase(t, store, "proj-lk-db", org)
	ctx := context.Background()

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, planbudget.OrgLockSpace, org); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- store.SampleOrg(ctx, org) }()
	select {
	case <-done:
		t.Fatal("the sample must wait for the step that holds the org lock")
	case <-time.After(500 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sample never ran after the lock was released")
	}
}

func TestAPauseIsSampledBeforeItChangesAnything(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "ob")
	fundOrg(t, store, org, "seed-ob", credits.FromDollars(5), nil)
	gate := gateOf(64000, 64*1024, 500, 50, true)
	gate.SetObserver(store.ObserveOrgUsage)
	store.SetPlanBudget(gate)
	newDatabase(t, store, "proj-ob-db", org)
	if rows := samplesOf(t, store, org); len(rows) == 0 {
		t.Fatal("the create is a step: the org is sampled before it too")
	}
	if _, err := store.db.Exec(`DELETE FROM usage_samples WHERE org_id = $1`, org); err != nil {
		t.Fatal(err)
	}

	paused := instanceRow("proj-ob-db", org)
	paused.Status = string(domain.StatusPaused)
	if err := store.UpdateIfStatus(paused, string(domain.StatusActive)); err != nil {
		t.Fatal(err)
	}
	rows := samplesOf(t, store, org)
	if len(rows) != 1 || rows[0].cpu != freeDatabaseCPU {
		t.Fatalf("the sample at a pause shows the service as it ran, got %+v", rows)
	}
}

func TestAFailingSampleInsideAStepNeverBreaksTheStep(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "fs")
	fundOrg(t, store, org, "seed-fs", credits.FromDollars(5), nil)
	gate := gateOf(64000, 64*1024, 500, 50, true)
	gate.SetObserver(store.ObserveOrgUsage)
	store.SetPlanBudget(gate)
	if _, err := store.db.Exec(`ALTER TABLE usage_samples RENAME TO usage_samples_gone`); err != nil {
		t.Fatal(err)
	}
	defer store.db.Exec(`ALTER TABLE usage_samples_gone RENAME TO usage_samples`) //nolint:errcheck // test cleanup
	if err := store.Create(instanceRow("proj-fs-db", org)); err != nil {
		t.Fatalf("a sampling failure must not refuse or abort the step: %v", err)
	}
	if got, _ := store.FindByProjectID("proj-fs-db"); got == nil {
		t.Fatal("the step's own write must commit")
	}
}

func TestMeterOrgsListsEveryCreditAccountWithWhenItsCreditAndSamplesBegan(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "mo")
	began := databaseNow(t, store).Add(-3 * time.Hour).Truncate(time.Second)
	fundAt(t, store, org, "seed-mo", credits.FromDollars(7), nil, began)
	orgs, err := store.MeterOrgs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 1 || orgs[0].OrgID != org || !orgs[0].Since.Equal(began) || orgs[0].FirstSample != nil {
		t.Fatalf("got %+v", orgs)
	}
	first := databaseNow(t, store).Truncate(5 * time.Minute)
	putSample(t, store, org, first, 100)
	orgs, err = store.MeterOrgs(context.Background())
	if err != nil || len(orgs) != 1 || orgs[0].FirstSample == nil || !orgs[0].FirstSample.Equal(first) {
		t.Fatalf("the org's first sample is reported: %+v %v", orgs, err)
	}
}

func TestUsageSlotsAreTheSamplesOfOneHalfOpenInterval(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "us")
	from := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	for i := range 14 { // 10:00 .. 11:05
		putSample(t, store, org, from.Add(time.Duration(i)*5*time.Minute), int64(100+i))
	}
	slots, err := store.UsageSlots(context.Background(), org, from, from.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 12 || slots[0].CPUMilli != 100 || slots[11].CPUMilli != 111 {
		t.Fatalf("10:00 to 10:55 inclusive, 11:00 excluded; got %d slots %+v", len(slots), slots)
	}
}

// meterRig is a store, an org with credit that began `hoursAgo` hours before the database's now,
// and a meter with the plan's prices and no allowance.
func meterRig(t *testing.T, person string, hoursAgo int, expiresAt func(began time.Time) *time.Time) (*Store, *creditmeter.Meter, string, time.Time) {
	t.Helper()
	store := testStore(t)
	org := creditPerson(t, store, person)
	began := databaseNow(t, store).Add(-time.Duration(hoursAgo) * time.Hour).Truncate(time.Hour).Add(-time.Minute)
	var expiry *time.Time
	if expiresAt != nil {
		expiry = expiresAt(began)
	}
	fundAt(t, store, org, "seed-"+person, credits.FromDollars(5), expiry, began)
	prices := creditmeter.Prices{Version: "v1", VCPUMonth: credits.FromDollars(12), RAMGiBMonth: credits.FromDollars(6), DiskGiBMonth: 120_000}
	meter := creditmeter.NewMeter(store, credits.NewService(store, trialRules(40), nil), prices)
	return store, meter, org, began.Add(time.Minute) // the first whole hour the org is billed from
}

func TestTheMeterBillsEachSettledHourOnceThroughTheRealLedger(t *testing.T) {
	store, meter, org, first := meterRig(t, "mt", 4, nil)
	for slot := first; slot.Before(first.Add(2 * time.Hour)); slot = slot.Add(5 * time.Minute) {
		putSample(t, store, org, slot, 250)
	}
	ctx := context.Background()
	for range 3 {
		if err := meter.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var entries int
	var amount int64
	if err := store.db.QueryRow(`SELECT count(*), COALESCE(sum(amount_micros), 0) FROM credit_entries WHERE org_id = $1 AND kind = 'usage'`, org).Scan(&entries, &amount); err != nil {
		t.Fatal(err)
	}
	if entries != 2 || amount != -2*4110 {
		t.Fatalf("two hours of a quarter vCPU, each 4110 micros and each once: %d entries, %d micros", entries, amount)
	}
	if balance := creditInvariant(t, store, org); balance != credits.FromDollars(5)-2*4110 {
		t.Fatalf("balance %d", balance)
	}
	mark, ok, err := store.CreditMeterWatermark(ctx, org)
	if err != nil || !ok || mark.Before(first.Add(2*time.Hour)) {
		t.Fatalf("the watermark must have passed the billed hours: %v %v %v", mark, ok, err)
	}
}

func TestAnHourIsNotBilledBeforeTenMinutesAfterItEndsOnTheDatabaseClock(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "tm")
	now := databaseNow(t, store)
	hourStart := now.Truncate(time.Hour)
	fundAt(t, store, org, "seed-tm", credits.FromDollars(5), nil, hourStart.Add(-time.Hour))
	for slot := hourStart.Add(-time.Hour); slot.Before(hourStart); slot = slot.Add(5 * time.Minute) {
		putSample(t, store, org, slot, 250)
	}
	prices := creditmeter.Prices{Version: "v1", VCPUMonth: credits.FromDollars(12)}
	meter := creditmeter.NewMeter(store, credits.NewService(store, trialRules(40), nil), prices)
	if err := meter.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var billed int
	if err := store.db.QueryRow(`SELECT count(*) FROM credit_entries WHERE org_id = $1 AND kind = 'usage'`, org).Scan(&billed); err != nil {
		t.Fatal(err)
	}
	wantBilled := 0
	if now.Sub(hourStart) >= 10*time.Minute {
		wantBilled = 1
	}
	if billed != wantBilled {
		t.Fatalf("at %v past the hour the previous hour is billed=%d, want %d", now.Sub(hourStart), billed, wantBilled)
	}
}

func TestTwoMetersRunningAtOnceWriteOneEntryPerHour(t *testing.T) {
	store, meter, org, first := meterRig(t, "cc", 3, nil)
	for slot := first; slot.Before(first.Add(time.Hour)); slot = slot.Add(5 * time.Minute) {
		putSample(t, store, org, slot, 250)
	}
	results := race([]func() error{
		func() error { return meter.Run(context.Background()) },
		func() error { return meter.Run(context.Background()) },
		func() error { return meter.Run(context.Background()) },
	})
	for _, err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var entries int
	if err := store.db.QueryRow(`SELECT count(*) FROM credit_entries WHERE org_id = $1 AND kind = 'usage'`, org).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("three racing meters, one hour, one entry: %d %v", entries, err)
	}
	creditInvariant(t, store, org)
}

func TestEveryHourTheCreditCannotPayKeepsItsShortfallAndLaterCreditNeverPaysForIt(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "zr")
	began := databaseNow(t, store).Add(-6 * time.Hour).Truncate(time.Hour).Add(-time.Minute)
	fundAt(t, store, org, "seed-zr", 5000, nil, began) // pays one hour of 4110 and part of the next
	first := began.Add(time.Minute)
	for slot := first; slot.Before(first.Add(4 * time.Hour)); slot = slot.Add(5 * time.Minute) {
		putSample(t, store, org, slot, 250)
	}
	prices := creditmeter.Prices{Version: "v1", VCPUMonth: credits.FromDollars(12)}
	ledger := credits.NewService(store, trialRules(40), nil)
	meter := creditmeter.NewMeter(store, ledger, prices)
	ctx := context.Background()
	if err := meter.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if balance := creditInvariant(t, store, org); balance != 0 {
		t.Fatalf("the credit is used up exactly, got %d", balance)
	}
	rows, err := store.db.Query(`SELECT amount_micros, COALESCE((details->>'uncovered_micros')::bigint, -1) FROM credit_entries WHERE org_id = $1 AND kind = 'usage' ORDER BY period_start`, org)
	if err != nil {
		t.Fatal(err)
	}
	var taken, shortfall []int64
	for rows.Next() {
		var amount, uncovered int64
		if err := rows.Scan(&amount, &uncovered); err != nil {
			t.Fatal(err)
		}
		taken, shortfall = append(taken, -amount), append(shortfall, uncovered)
	}
	rows.Close()
	if len(taken) != 4 || taken[0] != 4110 || taken[1] != 890 || taken[2] != 0 || taken[3] != 0 {
		t.Fatalf("the credit pays the first hour and part of the second: %v", taken)
	}
	if shortfall[0] != 0 || shortfall[1] != 3220 || shortfall[2] != 4110 || shortfall[3] != 4110 {
		t.Fatalf("each hour the credit could not pay records its shortfall: %v", shortfall)
	}
	// New credit arrives; the hours already passed are never billed again, so it pays only for later ones.
	fundAt(t, store, org, "seed-zr-2", 10_000, nil, databaseNow(t, store))
	if err := meter.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if balance := creditInvariant(t, store, org); balance != 10_000 {
		t.Fatalf("credit added later pays for none of the earlier hours, got %d", balance)
	}
}

// A lot expires only once the watermark has passed its date, and the hours before it are billed to it first.
func TestALotExpiresOnlyOnceTheMeterHasPassedItsDate(t *testing.T) {
	var expiry time.Time
	store, meter, org, first := meterRig(t, "ex", 6, func(began time.Time) *time.Time {
		expiry = began.Add(150 * time.Minute) // 2.5 hours in: inside the third hour
		return &expiry
	})
	for slot := first; slot.Before(first.Add(5 * time.Hour)); slot = slot.Add(5 * time.Minute) {
		putSample(t, store, org, slot, 250)
	}
	ctx := context.Background()
	now := databaseNow(t, store)
	// Before any metering the expiry date has passed on the clock, but not on the watermark.
	if n, err := store.ExpireCreditLots(ctx, now, true); err != nil || n != 0 {
		t.Fatalf("the lot must wait for the meter: expired %d, %v", n, err)
	}
	if err := meter.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := store.ExpireCreditLots(ctx, now, true); err != nil || n != 1 {
		t.Fatalf("once the watermark passes the date the lot expires: expired %d, %v", n, err)
	}
	if balance := creditInvariant(t, store, org); balance != 0 {
		t.Fatalf("the unused rest of the lot is expired, got %d", balance)
	}
	var billedToLot int64
	if err := store.db.QueryRow(`SELECT COALESCE(sum(d.amount_micros), 0) FROM credit_lot_debits d JOIN credit_entries e ON e.id = d.entry_id WHERE e.org_id = $1 AND e.kind = 'usage'`, org).Scan(&billedToLot); err != nil {
		t.Fatal(err)
	}
	if billedToLot != 2*4110 {
		t.Fatalf("hours 1 and 2 end before the lot expires and are billed to it; the third ends after: got %d", billedToLot)
	}
}

func TestSamplesOlderThanTheKeepWindowAndBilledArePruned(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "pr")
	now := databaseNow(t, store)
	old, recent := now.Add(-30*24*time.Hour).Truncate(5*time.Minute), now.Add(-time.Hour).Truncate(5*time.Minute)
	putSample(t, store, org, old, 100)
	putSample(t, store, org, recent, 100)
	other := creditPerson(t, store, "pu")
	putSample(t, store, other, old, 100)
	ctx := context.Background()
	if err := store.AdvanceCreditMeterWatermark(ctx, org, now); err != nil {
		t.Fatal(err)
	}
	// "pu" has billed up to a day before its old sample: that sample is the only record of what it owes, so it stays.
	if err := store.AdvanceCreditMeterWatermark(ctx, other, old.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PruneUsageSamples(ctx, 14*24*time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("one billed old sample is pruned: %d %v", removed, err)
	}
	if rows := samplesOf(t, store, org); len(rows) != 1 || !rows[0].slot.Equal(recent) {
		t.Fatalf("the recent sample stays: %+v", rows)
	}
	if rows := samplesOf(t, store, other); len(rows) != 1 {
		t.Fatalf("an unbilled sample is never pruned: %+v", rows)
	}
}

func TestTheBurnSumsUsagePerUTCDayAndSaysWhenItBegan(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "bn")
	fundOrg(t, store, org, "seed-bn", credits.FromDollars(5), nil)
	ctx := context.Background()
	debit := func(key string, start time.Time, amount credits.Micros) {
		t.Helper()
		if _, err := store.DebitCredit(ctx, credits.DebitRequest{OrgID: org, Amount: amount, PeriodStart: start, PeriodEnd: start.Add(time.Hour), IdempotencyKey: key}); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC)
	debit("b1", day.Add(23*time.Hour), 100)
	debit("b2", day.Add(24*time.Hour), 40)
	debit("b3", day.Add(25*time.Hour), 2)
	debit("old", day.Add(-48*time.Hour), 999)
	burn, err := store.CreditUsageBurn(ctx, org, day)
	if err != nil {
		t.Fatal(err)
	}
	if len(burn.Days) != 2 || burn.Days[0].Micros != 100 || burn.Days[1].Micros != 42 || !burn.Days[1].Day.Equal(day.AddDate(0, 0, 1)) {
		t.Fatalf("23:00 belongs to the 3rd; the next two to the 4th: %+v", burn.Days)
	}
	if burn.Since == nil || !burn.Since.Equal(day.Add(23*time.Hour)) {
		t.Fatalf("since %v", burn.Since)
	}
	if empty, err := store.CreditUsageBurn(ctx, "org-nobody", day); err != nil || len(empty.Days) != 0 || empty.Since != nil {
		t.Fatalf("an org with no usage: %+v %v", empty, err)
	}
}

func TestAnHourAlreadyBilledIsKeptWhenThePriceChangesBeforeTheRetry(t *testing.T) {
	store, meter, org, first := meterRig(t, "rp", 3, nil)
	for slot := first; slot.Before(first.Add(time.Hour)); slot = slot.Add(5 * time.Minute) {
		putSample(t, store, org, slot, 250)
	}
	ctx := context.Background()
	if err := meter.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM credit_meter_watermarks WHERE org_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	repriced := creditmeter.Prices{Version: "v2", VCPUMonth: credits.FromDollars(24), RAMGiBMonth: credits.FromDollars(6), DiskGiBMonth: 120_000}
	if err := creditmeter.NewMeter(store, credits.NewService(store, trialRules(40), nil), repriced).Run(ctx); err != nil {
		t.Fatalf("a retry at another price must not fail: %v", err)
	}
	var entries int
	var amount int64
	if err := store.db.QueryRow(`SELECT count(*), COALESCE(sum(amount_micros), 0) FROM credit_entries WHERE org_id = $1 AND kind = 'usage'`, org).Scan(&entries, &amount); err != nil {
		t.Fatal(err)
	}
	if entries != 1 || amount != -4110 {
		t.Fatalf("the hour stays billed once at the first price: %d entries, %d", entries, amount)
	}
	if mark, ok, err := store.CreditMeterWatermark(ctx, org); err != nil || !ok || mark.Before(first.Add(time.Hour)) {
		t.Fatalf("the watermark moved on past the billed hour: %v %v %v", mark, ok, err)
	}
}

func TestWithMeteringOffALotExpiresByTheClockEvenIfAWatermarkExists(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "of")
	now := databaseNow(t, store)
	expiry := now.Add(-2 * time.Hour)
	fundAt(t, store, org, "seed-of", credits.FromDollars(5), &expiry, now.Add(-10*time.Hour))
	ctx := context.Background()
	if err := store.AdvanceCreditMeterWatermark(ctx, org, now.Add(-5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := store.ExpireCreditLots(ctx, now, true); err != nil || n != 0 {
		t.Fatalf("while metering, a watermark before the date holds the lot: %d %v", n, err)
	}
	if n, err := store.ExpireCreditLots(ctx, now, false); err != nil || n != 1 {
		t.Fatalf("with metering off the clock decides: %d %v", n, err)
	}
	if balance := creditInvariant(t, store, org); balance != 0 {
		t.Fatalf("the lot is expired, got %d", balance)
	}
}

func TestAnOrgCreditedLongBeforeMeteringStartsAtItsFirstSampleNotAtItsCredit(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "fw")
	now := databaseNow(t, store)
	fundAt(t, store, org, "seed-fw", credits.FromDollars(5), nil, now.Add(-60*24*time.Hour))
	firstSlot := now.Add(-3 * time.Hour).Truncate(5 * time.Minute)
	for slot := firstSlot; slot.Before(now.Add(-time.Hour)); slot = slot.Add(5 * time.Minute) {
		putSample(t, store, org, slot, 250)
	}
	meter := creditmeter.NewMeter(store, credits.NewService(store, trialRules(40), nil), creditmeter.Prices{Version: "v1", VCPUMonth: credits.FromDollars(12)})
	if err := meter.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var earliest time.Time
	if err := store.db.QueryRow(`SELECT min(period_start) FROM credit_entries WHERE org_id = $1 AND kind = 'usage'`, org).Scan(&earliest); err != nil {
		t.Fatal(err)
	}
	if earliest.Before(firstSlot) {
		t.Fatalf("billing began at %v, before the first sample %v: sixty days of empty hours were walked", earliest, firstSlot)
	}
	mark, _, _ := store.CreditMeterWatermark(context.Background(), org)
	if mark.Before(firstSlot) {
		t.Fatalf("the watermark %v must not sit before the first sample", mark)
	}
}

func TestNothingBeforeMeteringWasEnabledIsBilled(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "en2")
	now := databaseNow(t, store)
	fundAt(t, store, org, "seed-en2", credits.FromDollars(5), nil, now.Add(-10*time.Hour))
	for slot := now.Add(-4 * time.Hour).Truncate(5 * time.Minute); slot.Before(now.Add(-time.Hour)); slot = slot.Add(5 * time.Minute) {
		putSample(t, store, org, slot, 250)
	}
	ctx := context.Background()
	if err := store.MarkMeteringEnabled(ctx); err != nil {
		t.Fatal(err)
	}
	enabled, err := store.MeteringEnabledAt(ctx)
	if err != nil || enabled.Before(now) {
		t.Fatalf("the enabling time is recorded at the first call: %v %v", enabled, err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := store.MarkMeteringEnabled(ctx); err != nil {
		t.Fatal(err)
	}
	if again, err := store.MeteringEnabledAt(ctx); err != nil || !again.Equal(enabled) {
		t.Fatalf("a later call changes nothing: %v then %v (%v)", enabled, again, err)
	}
	meter := creditmeter.NewMeter(store, credits.NewService(store, trialRules(40), nil), creditmeter.Prices{Version: "v1", VCPUMonth: credits.FromDollars(12)})
	if err := meter.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var entries int
	if err := store.db.QueryRow(`SELECT count(*) FROM credit_entries WHERE org_id = $1 AND kind = 'usage'`, org).Scan(&entries); err != nil || entries != 0 {
		t.Fatalf("usage sampled before metering was enabled is not billed: %d %v", entries, err)
	}
}

func TestAResumeIsSampledAgainOnceItHasTakenEffect(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "rs")
	fundOrg(t, store, org, "seed-rs", credits.FromDollars(5), nil)
	gate := gateOf(64000, 64*1024, 500, 50, true)
	gate.SetObserver(store.ObserveOrgUsage)
	store.SetPlanBudget(gate)
	paused := instanceRow("proj-rs-db", org)
	paused.Status = string(domain.StatusPaused)
	newDatabase(t, store, "proj-rs-db", org)
	if err := store.UpdateIfStatus(paused, string(domain.StatusActive)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM usage_samples WHERE org_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	resumed := instanceRow("proj-rs-db", org)
	if err := store.UpdateIfStatus(resumed, string(domain.StatusPaused)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		rows := samplesOf(t, store, org)
		if len(rows) == 1 && rows[0].cpu == freeDatabaseCPU {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the running state after a resume was never sampled: %+v", rows)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// testAllowance is the plan budget's allowance table: FREE holds 1 vCPU, 1 GiB and 10 GiB, STANDARD none; others are not metered.
func testAllowance(tier string) (creditmeter.Allowance, bool) {
	switch tier {
	case "FREE":
		return creditmeter.Allowance{CPUMilli: 1000, MemoryBytes: 1 << 30, DiskBytes: 10 << 30}, true
	case "STANDARD":
		return creditmeter.Allowance{}, true
	}
	return creditmeter.Allowance{}, false
}

// A free org sampled by the real sampler and billed by the real meter through the real store: nothing
// between the sample and the entry may drop what the sample says, least of all the allowance.
func TestAFreeOrgIsBilledOnlyAboveItsAllowanceEndToEndThroughTheRealStore(t *testing.T) {
	store, meter, org, hourStart := meterRig(t, "e2e", 4, nil)
	store.SetCreditAllowance(testAllowance)
	newDatabase(t, store, "proj-e2e-db", org)
	// 1250m of 1000m allowed, 1.5 GiB of 1 GiB, 12 GiB of 10 GiB: only the part above each is billed.
	if _, err := store.db.Exec(`UPDATE database_instances SET reserved_cpu_milli = 1250, reserved_memory_bytes = $1, reserved_disk_bytes = $2, storage_size = '12Gi' WHERE project_id = 'proj-e2e-db'`, 3<<29, 12<<30); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.SampleOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	// Make the one real sample the twelve slots of an hour that has settled.
	if _, err := store.db.Exec(`
		INSERT INTO usage_samples (org_id, slot_start, cpu_milli, memory_bytes, disk_bytes, allowance_cpu_milli, allowance_memory_bytes, allowance_disk_bytes)
		SELECT org_id, $2::timestamptz + n * interval '5 minutes', cpu_milli, memory_bytes, disk_bytes, allowance_cpu_milli, allowance_memory_bytes, allowance_disk_bytes
		FROM usage_samples, generate_series(0, 11) AS n WHERE org_id = $1`, org, hourStart.UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM usage_samples WHERE org_id = $1 AND slot_start > $2`, org, hourStart.UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := meter.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var amount int64
	var details []byte
	if err := store.db.QueryRow(`SELECT amount_micros, details::text FROM credit_entries WHERE org_id = $1 AND kind = 'usage'`, org).Scan(&amount, &details); err != nil {
		t.Fatalf("one hour, one entry: %v", err)
	}
	// 0.25 vCPU x $12 + 0.5 GiB x $6 + 2 GiB x $0.12 = $6.24 a month = 8547.95 micros an hour.
	if amount != -8548 {
		t.Fatalf("only what is above the allowance is billed: want -8548, got %d (%s)", amount, details)
	}
	for _, want := range []string{`"priceVersion": "v1"`, `"slots": 12`, `"cpuMilliSlots": 3000`} {
		if !strings.Contains(string(details), want) {
			t.Errorf("the entry must carry %s, got %s", want, details)
		}
	}
}

func TestAnAllowanceInsideOnlyTheMemoryDimensionIsStillReadBack(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "ad")
	slot := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`INSERT INTO usage_samples (org_id, slot_start, cpu_milli, memory_bytes, disk_bytes, allowance_cpu_milli, allowance_memory_bytes, allowance_disk_bytes) VALUES ($1, $2, 1, 2, 3, 4, 5, 6)`, org, slot); err != nil {
		t.Fatal(err)
	}
	slots, err := store.UsageSlots(context.Background(), org, slot, slot.Add(time.Hour))
	if err != nil || len(slots) != 1 {
		t.Fatalf("%v %v", slots, err)
	}
	want := creditmeter.Slot{CPUMilli: 1, MemoryBytes: 2, DiskBytes: 3, Allowance: creditmeter.Allowance{CPUMilli: 4, MemoryBytes: 5, DiskBytes: 6}}
	if slots[0] != want {
		t.Fatalf("every column of a sample must come back: want %+v, got %+v", want, slots[0])
	}
}

func TestPostStepSamplesNeverUseUpThePool(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "pl")
	fundOrg(t, store, org, "seed-pl", credits.FromDollars(5), nil)
	store.db.SetMaxOpenConns(5)
	ctx := context.Background()

	step, err := store.db.BeginTx(ctx, nil) // a step that holds the org lock for a while
	if err != nil {
		t.Fatal(err)
	}
	defer step.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	if _, err := step.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, planbudget.OrgLockSpace, org); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		store.sampleAfterStep(org)
	}
	time.Sleep(600 * time.Millisecond) // let the waiting samples settle in
	var waiting int
	if err := store.db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if waiting != 0 {
		t.Fatalf("a post-step sample must never sit on a connection waiting for the org lock: %d are", waiting)
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := store.db.PingContext(probe); err != nil {
		t.Fatalf("samples waiting for a step's lock must not use up the pool: %v", err)
	}
	if err := step.Commit(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for len(samplesOf(t, store, org)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the sample never landed after the step committed")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func creditMeterEventCount(t *testing.T, reason string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "excalibase_credit_meter_events_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetValue() == reason {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestAPostStepSampleThatGivesUpIsCounted(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "gu")
	fundOrg(t, store, org, "seed-gu", credits.FromDollars(5), nil)
	ctx := context.Background()
	step, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer step.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	if _, err := step.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, planbudget.OrgLockSpace, org); err != nil {
		t.Fatal(err)
	}
	tries, every := postStepTries, postStepRetryEvery
	postStepTries, postStepRetryEvery = 3, 10*time.Millisecond
	defer func() { postStepTries, postStepRetryEvery = tries, every }()

	before := creditMeterEventCount(t, metrics.CreditMeterDroppedPostStepSample)
	if err := store.sampleWhenFree(ctx, org); err != nil {
		t.Fatal(err)
	}
	if got := creditMeterEventCount(t, metrics.CreditMeterDroppedPostStepSample) - before; got != 1 {
		t.Fatalf("a sample that gave up is counted once, got %v", got)
	}
}
