//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/excalibase/provisioning-poc/internal/credits"
)

func TestCreditLedgerRefusesUpdateDeleteAndTruncate(t *testing.T) {
	store := testStore(t)
	fundOrg(t, store, "org-ledger", "seed-1", 1000, nil)
	if _, err := store.DebitCredit(context.Background(), credits.DebitRequest{OrgID: "org-ledger", Amount: 10, PeriodStart: creditsNow, PeriodEnd: creditsNow.Add(time.Hour), IdempotencyKey: "u-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO credit_grant_claims (claim_kind, claim_value, user_id, org_id) VALUES ('user', 'u-ledger', 'u-ledger', 'org-ledger')`); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`UPDATE credit_entries SET amount_micros = 1`, `DELETE FROM credit_entries`, `TRUNCATE credit_entries`,
		`UPDATE credit_lot_debits SET amount_micros = 1`, `DELETE FROM credit_lot_debits`, `TRUNCATE credit_lot_debits`,
		`UPDATE credit_grant_claims SET user_id = 'x'`, `DELETE FROM credit_grant_claims`, `TRUNCATE credit_grant_claims`,
	}
	for _, statement := range statements {
		_, err := store.db.Exec(statement)
		var pqErr *pq.Error
		if !errors.As(err, &pqErr) || pqErr.Code.Name() != "restrict_violation" {
			t.Errorf("%s: want the append-only refusal, got %v", statement, err)
		}
	}
	if _, err := store.db.Exec(`UPDATE credit_accounts SET balance_micros = -1 WHERE org_id = 'org-ledger'`); err == nil {
		t.Error("a negative balance was accepted by the table")
	}
}

func TestOrgDeleteSucceedsAndTheLedgerStays(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()
	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-doomed", "org-doomed", userID, "STANDARD"), 0); err != nil {
		t.Fatal(err)
	}
	fundOrg(t, store, "org-doomed", "seed-doomed", 5000, nil)
	if err := store.DeleteOrg(ctx, "org-doomed"); err != nil {
		t.Fatalf("an org with credit must delete without a foreign-key refusal: %v", err)
	}
	var entries int
	if err := store.db.QueryRow(`SELECT count(*) FROM credit_entries WHERE org_id = 'org-doomed'`).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("the ledger must outlive the org: %d %v", entries, err)
	}
}

func TestDebitDrawsFromTheSoonestExpiringLotAndNeverGoesNegative(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	fundOrg(t, store, "org-d", "a", 100, nil)
	fundOrg(t, store, "org-d", "b", 100, inHours(48))
	fundOrg(t, store, "org-d", "c", 100, inHours(24))
	request := credits.DebitRequest{OrgID: "org-d", Amount: 150, PeriodStart: creditsNow, PeriodEnd: creditsNow.Add(time.Hour), IdempotencyKey: "u-1"}
	result, err := store.DebitCredit(ctx, request)
	if err != nil || result.Taken != 150 || result.Uncovered != 0 {
		t.Fatalf("debit: %+v %v", result, err)
	}
	account, _ := store.CreditAccount(ctx, "org-d")
	remaining := map[credits.Micros]credits.Micros{}
	for _, lot := range account.Lots {
		remaining[lot.Original] += lot.Remaining
	}
	if account.Balance != 150 {
		t.Fatalf("balance %d, want 150", account.Balance)
	}
	soonest := lotRemainingByExpiry(t, store, "org-d")
	if soonest[24] != 0 || soonest[48] != 50 || soonest[-1] != 100 {
		t.Fatalf("the 24h lot must be drained first, then the 48h one: %v", soonest)
	}
	creditInvariant(t, store, "org-d")

	again, err := store.DebitCredit(ctx, request)
	if err != nil || !again.Replayed || again.Taken != 150 {
		t.Fatalf("a replay returns the first result without charging again: %+v %v", again, err)
	}
	if balance := creditInvariant(t, store, "org-d"); balance != 150 {
		t.Fatalf("a replay changed the balance to %d", balance)
	}

	big := request
	big.IdempotencyKey, big.Amount = "u-2", 10_000
	result, err = store.DebitCredit(ctx, big)
	if err != nil || result.Taken != 150 || result.Uncovered != 9850 {
		t.Fatalf("the shortfall is recorded, not taken: %+v %v", result, err)
	}
	if balance := creditInvariant(t, store, "org-d"); balance != 0 {
		t.Fatalf("balance %d, want 0 and never below", balance)
	}
}

func lotRemainingByExpiry(t *testing.T, store *Store, orgID string) map[int]credits.Micros {
	t.Helper()
	rows, err := store.db.Query(`SELECT expires_at, remaining_micros FROM credit_lots WHERE org_id = $1`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int]credits.Micros{}
	for rows.Next() {
		var expires *time.Time
		var remaining int64
		if err := rows.Scan(&expires, &remaining); err != nil {
			t.Fatal(err)
		}
		hours := -1
		if expires != nil {
			hours = int(expires.Sub(creditsNow).Hours())
		}
		out[hours] = credits.Micros(remaining)
	}
	return out
}

func TestDebitSkipsLotsThatExpireBeforeThePeriodEnds(t *testing.T) {
	store := testStore(t)
	fundOrg(t, store, "org-skip", "a", 100, inHours(2))
	fundOrg(t, store, "org-skip", "b", 100, inHours(30))
	result, err := store.DebitCredit(context.Background(), credits.DebitRequest{OrgID: "org-skip", Amount: 150, PeriodStart: creditsNow.Add(5 * time.Hour), PeriodEnd: creditsNow.Add(6 * time.Hour), IdempotencyKey: "u-1"})
	if err != nil || result.Taken != 100 || result.Uncovered != 50 {
		t.Fatalf("only the lot good at the period end pays: %+v %v", result, err)
	}
}

func TestDebitConcurrentWithTheSameKeyChargesOnce(t *testing.T) {
	store := testStore(t)
	fundOrg(t, store, "org-race", "a", 1000, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.DebitCredit(context.Background(), credits.DebitRequest{OrgID: "org-race", Amount: 100, PeriodStart: creditsNow, PeriodEnd: creditsNow.Add(time.Hour), IdempotencyKey: "same"})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if balance := creditInvariant(t, store, "org-race"); balance != 900 {
		t.Fatalf("balance %d, want 900: one charge", balance)
	}
}

func TestLotExpiresOnlyOncetheMeterHasPassedIt(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	fundOrg(t, store, "org-w", "a", 500, inHours(10))
	later := creditsNow.Add(20 * time.Hour)

	if err := store.AdvanceCreditMeterWatermark(ctx, "org-w", creditsNow.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := store.ExpireCreditLots(ctx, later, false); err != nil || n != 0 {
		t.Fatalf("the meter has not reached the expiry: expired %d %v", n, err)
	}
	if balance := creditInvariant(t, store, "org-w"); balance != 500 {
		t.Fatalf("balance %d, want 500", balance)
	}
	if err := store.AdvanceCreditMeterWatermark(ctx, "org-w", creditsNow.Add(11*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := store.ExpireCreditLots(ctx, later, false); err != nil || n != 1 {
		t.Fatalf("the meter passed the expiry: expired %d %v", n, err)
	}
	if n, _ := store.ExpireCreditLots(ctx, later, false); n != 0 {
		t.Fatal("a lot expired twice")
	}
	if balance := creditInvariant(t, store, "org-w"); balance != 0 {
		t.Fatalf("balance %d, want 0", balance)
	}
	if mark, ok, err := store.CreditMeterWatermark(ctx, "org-w"); err != nil || !ok || !mark.Equal(creditsNow.Add(11*time.Hour)) {
		t.Fatalf("watermark %v %v %v", mark, ok, err)
	}
}

func TestLotExpiresByTheClockWhenNoMeterHasStarted(t *testing.T) {
	store := testStore(t)
	fundOrg(t, store, "org-nometer", "a", 500, inHours(10))
	if n, err := store.ExpireCreditLots(context.Background(), creditsNow.Add(11*time.Hour), false); err != nil || n != 1 {
		t.Fatalf("an unmetered lot expires on its date: %d %v", n, err)
	}
	if n, _ := store.ExpireCreditLots(context.Background(), creditsNow.Add(5*time.Hour), false); n != 0 {
		t.Fatal("a lot expired before its date")
	}
}

func TestWatermarkNeverMovesBackward(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	_ = store.AdvanceCreditMeterWatermark(ctx, "org-m", creditsNow.Add(5*time.Hour))
	_ = store.AdvanceCreditMeterWatermark(ctx, "org-m", creditsNow)
	mark, _, _ := store.CreditMeterWatermark(ctx, "org-m")
	if !mark.Equal(creditsNow.Add(5 * time.Hour)) {
		t.Fatalf("watermark moved back to %v", mark)
	}
}

// propertyOrg is the org the random operations run on.
const propertyOrg = "org-prop"

// propertyStep is one random operation on the ledger.
type propertyStep struct {
	t      *testing.T
	store  *Store
	random *rand.Rand
	clock  time.Time
	key    string
	number int
}

func (s propertyStep) grant() {
	var expires *time.Time
	if s.random.Intn(2) == 0 {
		at := s.clock.Add(time.Duration(1+s.random.Intn(40)) * time.Hour)
		expires = &at
	}
	adjustForTest(s.t, s.store, propertyOrg, credits.Micros(1+s.random.Intn(500)), s.key, expires, s.clock)
}

func (s propertyStep) takeBack() {
	_, err := s.store.AdjustCredit(context.Background(), negativeAdjustment(propertyOrg, credits.Micros(1+s.random.Intn(300)), s.key), s.clock)
	if err != nil && !errors.Is(err, credits.ErrInsufficientBalance) {
		s.t.Fatalf("step %d: %v", s.number, err)
	}
}

func (s propertyStep) debit() {
	end := s.clock.Add(time.Duration(1+s.random.Intn(10)) * time.Hour)
	request := credits.DebitRequest{OrgID: propertyOrg, Amount: credits.Micros(1 + s.random.Intn(400)), PeriodStart: s.clock, PeriodEnd: end, IdempotencyKey: s.key}
	if _, err := s.store.DebitCredit(context.Background(), request); err != nil {
		s.t.Fatalf("step %d debit: %v", s.number, err)
	}
	_ = s.store.AdvanceCreditMeterWatermark(context.Background(), propertyOrg, end)
}

func (s propertyStep) expire() {
	if _, err := s.store.ExpireCreditLots(context.Background(), s.clock, false); err != nil {
		s.t.Fatalf("step %d expire: %v", s.number, err)
	}
}

// Σ lots.remaining = balance = Σ entries after any mix of grants, debits,
// negative adjustments and expiries.
func TestCreditLedgerInvariantHoldsUnderRandomOperations(t *testing.T) {
	store := testStore(t)
	random := rand.New(rand.NewSource(42))
	clock := creditsNow
	for number := 0; number < 150; number++ {
		clock = clock.Add(time.Duration(random.Intn(6)) * time.Hour)
		step := propertyStep{t: t, store: store, random: random, clock: clock, key: fmt.Sprintf("op-%d", number), number: number}
		operations := []func(){step.grant, step.takeBack, step.debit, step.debit, step.expire}
		operations[random.Intn(len(operations))]()
		if balance := creditInvariant(t, store, propertyOrg); balance < 0 {
			t.Fatalf("step %d: balance %d", number, balance)
		}
	}
}

func adjustForTest(t *testing.T, store *Store, orgID string, amount credits.Micros, key string, expires *time.Time, now time.Time) {
	t.Helper()
	_, err := store.AdjustCredit(context.Background(), credits.Adjustment{OrgID: orgID, Amount: amount, Actor: "admin-1", Reason: "property test grant", IdempotencyKey: key, ExpiresAt: expires,
		Audit: credits.AuditRecord{UserID: "admin-1", Action: "admin.credit_adjust", Resource: "org"}}, now)
	if err != nil {
		t.Fatal(err)
	}
}

func negativeAdjustment(orgID string, amount credits.Micros, key string) credits.Adjustment {
	return credits.Adjustment{OrgID: orgID, Amount: -amount, Actor: "admin-1", Reason: "claw back for a test", IdempotencyKey: key,
		Audit: credits.AuditRecord{UserID: "admin-1", Action: "admin.credit_adjust", Resource: "org"}}
}

// Once a meter runs, an org it has not started on must not lose credit to the clock: the hours before
// the expiry are not billed yet.
func TestWithMeteringOnALotWaitsForItsOrgsFirstWatermark(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	fundOrg(t, store, "org-gm", "a", 500, inHours(10))
	later := creditsNow.Add(20 * time.Hour)
	if n, err := store.ExpireCreditLots(ctx, later, true); err != nil || n != 0 {
		t.Fatalf("no watermark and metering on: expired %d %v", n, err)
	}
	if err := store.AdvanceCreditMeterWatermark(ctx, "org-gm", later); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.ExpireCreditLots(ctx, later, true); n != 1 {
		t.Fatal("a metered org's lot expires once the watermark passes it")
	}
}

// watermarkCase is an org with one lot that ends 10 hours after creditsNow, a meter at some hour (or none),
// and the answer a sweep run at hour 20 must give for each way of asking.
type watermarkCase struct {
	name             string
	meterHour        int
	hasMeter         bool
	requireWatermark bool
	expires          bool
}

var watermarkCases = []watermarkCase{
	{name: "no meter, clock rules", expires: true},
	{name: "no meter, metering on", requireWatermark: true},
	{name: "meter behind the expiry, clock rules", hasMeter: true, meterHour: 5},
	{name: "meter behind the expiry, metering on", hasMeter: true, meterHour: 5, requireWatermark: true},
	{name: "meter past the expiry, clock rules", hasMeter: true, meterHour: 11, expires: true},
	{name: "meter past the expiry, metering on", hasMeter: true, meterHour: 11, requireWatermark: true, expires: true},
}

func watermarkOrg(t *testing.T, store *Store, c watermarkCase, orgID string) int64 {
	t.Helper()
	fundOrg(t, store, orgID, "wm", 500, inHours(10))
	if c.hasMeter {
		if err := store.AdvanceCreditMeterWatermark(context.Background(), orgID, creditsNow.Add(time.Duration(c.meterHour)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	var lotID int64
	if err := store.db.QueryRow(`SELECT id FROM credit_lots WHERE org_id = $1`, orgID).Scan(&lotID); err != nil {
		t.Fatal(err)
	}
	return lotID
}

// The sweep that picks the due lots honours the watermark rule by itself.
func TestBatchExpiryHonoursTheWatermarkRuleInEveryCase(t *testing.T) {
	later := creditsNow.Add(20 * time.Hour)
	for i, c := range watermarkCases {
		store := testStore(t)
		watermarkOrg(t, store, c, fmt.Sprintf("org-wb%d", i))
		n, err := store.ExpireCreditLots(context.Background(), later, c.requireWatermark)
		if err != nil || (n == 1) != c.expires {
			t.Errorf("%s: expired %d %v, want expired=%v", c.name, n, err, c.expires)
		}
	}
}

// The step that expires one lot re-checks the same rule itself, so a lot the sweep picked a moment too early is not taken.
func TestSingleLotExpiryHonoursTheWatermarkRuleInEveryCase(t *testing.T) {
	later := creditsNow.Add(20 * time.Hour)
	for i, c := range watermarkCases {
		store := testStore(t)
		orgID := fmt.Sprintf("org-wl%d", i)
		lotID := watermarkOrg(t, store, c, orgID)
		done, err := store.expireCreditLot(context.Background(), lotID, orgID, later, c.requireWatermark)
		if err != nil || done != c.expires {
			t.Errorf("%s: expired=%v %v, want %v", c.name, done, err, c.expires)
		}
	}
}
