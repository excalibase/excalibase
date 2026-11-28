package creditmeter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/credits"
)

var day = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

type fakeStore struct {
	now   time.Time
	orgs  []MeterOrg
	slots func(orgID string, from, to time.Time) ([]Slot, error)
}

func (f *fakeStore) MeterNow(context.Context) (time.Time, error)   { return f.now, nil }
func (f *fakeStore) MeterOrgs(context.Context) ([]MeterOrg, error) { return f.orgs, nil }
func (f *fakeStore) UsageSlots(_ context.Context, orgID string, from, to time.Time) ([]Slot, error) {
	return f.slots(orgID, from, to)
}

// fakeLedger bills by key like the real ledger: a key seen before returns what it did.
type fakeLedger struct {
	marks   map[string]time.Time
	debits  map[string]credits.DebitRequest
	order   []credits.DebitRequest
	covered credits.Micros // how much the lots can still pay
}

func newLedger(covered credits.Micros) *fakeLedger {
	return &fakeLedger{marks: map[string]time.Time{}, debits: map[string]credits.DebitRequest{}, covered: covered}
}

func (l *fakeLedger) Debit(_ context.Context, request credits.DebitRequest) (credits.DebitResult, error) {
	if _, seen := l.debits[request.IdempotencyKey]; seen {
		return credits.DebitResult{Replayed: true}, nil
	}
	l.debits[request.IdempotencyKey] = request
	l.order = append(l.order, request)
	taken := min(request.Amount, l.covered)
	l.covered -= taken
	return credits.DebitResult{Taken: taken, Uncovered: request.Amount - taken}, nil
}

func (l *fakeLedger) MeterWatermark(_ context.Context, orgID string) (time.Time, bool, error) {
	mark, ok := l.marks[orgID]
	return mark, ok, nil
}

func (l *fakeLedger) AdvanceMeterWatermark(_ context.Context, orgID string, to time.Time) error {
	if to.After(l.marks[orgID]) {
		l.marks[orgID] = to
	}
	return nil
}

// busy is a full hour of a quarter vCPU, 12 slots; every hour costs 4110 micros.
func busy(string, time.Time, time.Time) ([]Slot, error) { return slots(12, Slot{CPUMilli: 250}), nil }

const quarterHour = credits.Micros(4110)

func newMeter(store *fakeStore, ledger *fakeLedger) *Meter {
	return NewMeter(store, ledger, planPrices, func(string) Allowance { return Allowance{} })
}

func org(since time.Time) MeterOrg {
	return MeterOrg{OrgID: "org-1", Tier: "FREE", Balance: credits.FromDollars(10), Since: since}
}

func TestAnHourIsBilledOnlyTenMinutesAfterItEnds(t *testing.T) {
	ledger := newLedger(credits.FromDollars(10))
	store := &fakeStore{now: day.Add(13*time.Hour + 9*time.Minute), orgs: []MeterOrg{org(day.Add(12 * time.Hour))}, slots: busy}
	if err := newMeter(store, ledger).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ledger.order) != 0 {
		t.Fatalf("hour 12:00 must wait until 13:10, but %d debits were made at 13:09", len(ledger.order))
	}
	store.now = day.Add(13*time.Hour + 10*time.Minute)
	if err := newMeter(store, ledger).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ledger.order) != 1 {
		t.Fatalf("hour 12:00 is due at 13:10, got %d debits", len(ledger.order))
	}
}

func TestADebitCoversExactlyTheHourAndCarriesTheKeyAndPriceVersion(t *testing.T) {
	ledger := newLedger(credits.FromDollars(10))
	store := &fakeStore{now: day.Add(13*time.Hour + 30*time.Minute), orgs: []MeterOrg{org(day.Add(12 * time.Hour))}, slots: busy}
	if err := newMeter(store, ledger).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := ledger.order[0]
	if !got.PeriodStart.Equal(day.Add(12*time.Hour)) || !got.PeriodEnd.Equal(day.Add(13*time.Hour)) {
		t.Fatalf("period must be 12:00-13:00 UTC, got %v to %v", got.PeriodStart, got.PeriodEnd)
	}
	if got.IdempotencyKey != "usage:org-1:2026-12-01T12:00:00Z" || got.Amount != quarterHour || got.Details["priceVersion"] != "v1" {
		t.Fatalf("unexpected debit %+v", got)
	}
	if mark := ledger.marks["org-1"]; !mark.Equal(day.Add(13 * time.Hour)) {
		t.Fatalf("watermark must move to 13:00, got %v", mark)
	}
}

func TestRunningAgainBillsNothingMore(t *testing.T) {
	ledger := newLedger(credits.FromDollars(10))
	store := &fakeStore{now: day.Add(14*time.Hour + 30*time.Minute), orgs: []MeterOrg{org(day.Add(12 * time.Hour))}, slots: busy}
	meter := newMeter(store, ledger)
	for range 3 {
		if err := meter.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(ledger.order) != 2 {
		t.Fatalf("hours 12 and 13 once each, got %d debits", len(ledger.order))
	}
}

func TestACrashBetweenTheDebitAndTheWatermarkBillsTheHourOnce(t *testing.T) {
	ledger := newLedger(credits.FromDollars(10))
	store := &fakeStore{now: day.Add(13*time.Hour + 30*time.Minute), orgs: []MeterOrg{org(day.Add(12 * time.Hour))}, slots: busy}
	meter := newMeter(store, ledger)
	if err := meter.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	delete(ledger.marks, "org-1") // the watermark write was lost
	if err := meter.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ledger.order) != 1 {
		t.Fatalf("the replayed hour must not bill twice, got %d debits", len(ledger.order))
	}
}

func TestCatchingUpAfterDowntimeBillsEachMissedHourOnceInOrder(t *testing.T) {
	ledger := newLedger(credits.FromDollars(10))
	ledger.marks["org-1"] = day.Add(10 * time.Hour)
	store := &fakeStore{now: day.Add(13*time.Hour + 15*time.Minute), orgs: []MeterOrg{org(day)}, slots: busy}
	if err := newMeter(store, ledger).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ledger.order) != 3 {
		t.Fatalf("hours 10, 11 and 12, got %d", len(ledger.order))
	}
	for i, hour := range []int{10, 11, 12} {
		if !ledger.order[i].PeriodStart.Equal(day.Add(time.Duration(hour) * time.Hour)) {
			t.Fatalf("debit %d is for %v", i, ledger.order[i].PeriodStart)
		}
	}
}

func TestAnOrgNeverMeteredStartsAtTheFirstWholeHourAfterItsCreditBegan(t *testing.T) {
	ledger := newLedger(credits.FromDollars(10))
	began := day.Add(9*time.Hour + 20*time.Minute)
	store := &fakeStore{now: day.Add(12 * time.Hour), orgs: []MeterOrg{org(began)}, slots: busy}
	if err := newMeter(store, ledger).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if first := ledger.order[0].PeriodStart; !first.Equal(day.Add(10 * time.Hour)) {
		t.Fatalf("usage before the credit existed is not billed: first hour %v", first)
	}
}

func TestAnHourWithNothingToBillMovesTheWatermarkWithoutAnEntry(t *testing.T) {
	ledger := newLedger(credits.FromDollars(10))
	idle := func(string, time.Time, time.Time) ([]Slot, error) { return nil, nil }
	store := &fakeStore{now: day.Add(13*time.Hour + 30*time.Minute), orgs: []MeterOrg{org(day.Add(12 * time.Hour))}, slots: idle}
	if err := newMeter(store, ledger).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ledger.order) != 0 || !ledger.marks["org-1"].Equal(day.Add(13*time.Hour)) {
		t.Fatalf("no entry and a moved watermark expected, got %d debits and %v", len(ledger.order), ledger.marks)
	}
}

func TestTheTiersAllowanceIsSubtracted(t *testing.T) {
	ledger := newLedger(credits.FromDollars(10))
	store := &fakeStore{now: day.Add(13*time.Hour + 30*time.Minute), orgs: []MeterOrg{org(day.Add(12 * time.Hour))}, slots: busy}
	allow := func(tier string) Allowance {
		if tier == "FREE" {
			return Allowance{CPUMilli: 250}
		}
		return Allowance{}
	}
	if err := NewMeter(store, ledger, planPrices, allow).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ledger.order) != 0 {
		t.Fatalf("a quarter vCPU is inside the FREE allowance, got %d debits", len(ledger.order))
	}
}

func TestAnOrgOutOfCreditIsNotDebitedButItsWatermarkStillMoves(t *testing.T) {
	ledger := newLedger(0)
	empty := org(day.Add(12 * time.Hour))
	empty.Balance = 0
	store := &fakeStore{now: day.Add(14*time.Hour + 30*time.Minute), orgs: []MeterOrg{empty}, slots: busy}
	if err := newMeter(store, ledger).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ledger.order) != 0 {
		t.Fatalf("a zero balance is never debited, got %d debits", len(ledger.order))
	}
	if !ledger.marks["org-1"].Equal(day.Add(14 * time.Hour)) {
		t.Fatalf("credit added later must not pay for the empty hours: watermark %v", ledger.marks["org-1"])
	}
}

func TestOnceTheCreditRunsOutTheRestOfThePassStopsDebiting(t *testing.T) {
	ledger := newLedger(quarterHour) // pays one hour only
	store := &fakeStore{now: day.Add(15*time.Hour + 30*time.Minute), orgs: []MeterOrg{org(day.Add(12 * time.Hour))}, slots: busy}
	if err := newMeter(store, ledger).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Hour 12 paid in full; hour 13 is the one the credit ran out in; later hours are not debited.
	if len(ledger.order) != 2 {
		t.Fatalf("expected the paid hour and the hour that ran out, got %d debits", len(ledger.order))
	}
	if !ledger.marks["org-1"].Equal(day.Add(15 * time.Hour)) {
		t.Fatalf("every due hour is passed, watermark %v", ledger.marks["org-1"])
	}
}

func TestOneOrgFailingDoesNotStopTheOthers(t *testing.T) {
	ledger := newLedger(credits.FromDollars(10))
	good, bad := org(day.Add(12*time.Hour)), org(day.Add(12*time.Hour))
	good.OrgID, bad.OrgID = "good", "bad"
	store := &fakeStore{now: day.Add(13*time.Hour + 30*time.Minute), orgs: []MeterOrg{bad, good}, slots: func(orgID string, from, to time.Time) ([]Slot, error) {
		if orgID == "bad" {
			return nil, errors.New("samples unreadable")
		}
		return busy(orgID, from, to)
	}}
	err := newMeter(store, ledger).Run(context.Background())
	if err == nil {
		t.Fatal("the failure must be reported")
	}
	if len(ledger.order) != 1 || ledger.order[0].OrgID != "good" {
		t.Fatalf("the healthy org is still billed, got %+v", ledger.order)
	}
	if _, moved := ledger.marks["bad"]; moved {
		t.Fatal("a failed hour must not move the watermark")
	}
}

func TestAPassBillsAtMostAWeekOfHoursPerOrg(t *testing.T) {
	ledger := newLedger(credits.FromDollars(1000))
	store := &fakeStore{now: day.Add(24 * 30 * time.Hour), orgs: []MeterOrg{org(day)}, slots: busy}
	if err := newMeter(store, ledger).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ledger.order) != maxHoursPerPass {
		t.Fatalf("a pass is capped at %d hours, got %d", maxHoursPerPass, len(ledger.order))
	}
}
