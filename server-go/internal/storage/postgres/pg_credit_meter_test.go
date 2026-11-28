//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/creditmeter"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/domain"
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

func TestADatabaseWhoseSizeIsUnknownSamplesNothingAndSaysSo(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "uk")
	fundOrg(t, store, org, "seed-uk", credits.FromDollars(5), nil)
	newDatabase(t, store, "proj-uk-db", org)
	if _, err := store.db.Exec(`UPDATE database_instances SET reserved_copies = 0 WHERE project_id = 'proj-uk-db'`); err != nil {
		t.Fatal(err)
	}
	err := store.SampleOrg(context.Background(), org)
	if !errors.Is(err, planbudget.ErrUnrecordedSize) {
		t.Fatalf("an unreadable size must be reported, got %v", err)
	}
	if rows := samplesOf(t, store, org); len(rows) != 0 {
		t.Fatalf("a guess is never written, got %+v", rows)
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

func TestMeterOrgsListsEveryCreditAccountWithItsPlanAndBalance(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "mo")
	began := databaseNow(t, store).Add(-3 * time.Hour).Truncate(time.Second)
	fundAt(t, store, org, "seed-mo", credits.FromDollars(7), nil, began)
	orgs, err := store.MeterOrgs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 1 || orgs[0].OrgID != org || orgs[0].Tier != "FREE" || orgs[0].Balance != credits.FromDollars(7) || !orgs[0].Since.Equal(began) {
		t.Fatalf("got %+v", orgs)
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
	meter := creditmeter.NewMeter(store, credits.NewService(store, trialRules(40), nil), prices, func(string) creditmeter.Allowance { return creditmeter.Allowance{} })
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
	meter := creditmeter.NewMeter(store, credits.NewService(store, trialRules(40), nil), prices, func(string) creditmeter.Allowance { return creditmeter.Allowance{} })
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

func TestACreditThatRunsOutNeverGoesBelowZeroAndTheMeterKeepsMoving(t *testing.T) {
	store := testStore(t)
	org := creditPerson(t, store, "zr")
	began := databaseNow(t, store).Add(-5 * time.Hour).Truncate(time.Hour).Add(-time.Minute)
	fundAt(t, store, org, "seed-zr", 5000, nil, began) // pays one hour of 4110 and part of the next
	first := began.Add(time.Minute)
	for slot := first; slot.Before(first.Add(3 * time.Hour)); slot = slot.Add(5 * time.Minute) {
		putSample(t, store, org, slot, 250)
	}
	prices := creditmeter.Prices{Version: "v1", VCPUMonth: credits.FromDollars(12)}
	meter := creditmeter.NewMeter(store, credits.NewService(store, trialRules(40), nil), prices, func(string) creditmeter.Allowance { return creditmeter.Allowance{} })
	for range 2 {
		if err := meter.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if balance := creditInvariant(t, store, org); balance != 0 {
		t.Fatalf("the credit is used up exactly, got %d", balance)
	}
	var entries int
	if err := store.db.QueryRow(`SELECT count(*) FROM credit_entries WHERE org_id = $1 AND kind = 'usage'`, org).Scan(&entries); err != nil || entries != 2 {
		t.Fatalf("the paid hour and the hour the credit ran out in, nothing after: %d %v", entries, err)
	}
	mark, _, _ := store.CreditMeterWatermark(context.Background(), org)
	if mark.Before(first.Add(3 * time.Hour)) {
		t.Fatalf("every settled hour is passed even with nothing to pay it: %v", mark)
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
