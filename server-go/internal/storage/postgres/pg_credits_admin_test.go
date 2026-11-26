//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/credits"
)

func auditCount(t *testing.T, store *Store, action string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAdminGrantWritesItsAuditRowInTheSameTransaction(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	entry := fundOrg(t, store, "org-a", "k-1", 2500, nil)
	if entry.Amount != 2500 || entry.Kind != credits.KindGrantAdmin || entry.Actor != "admin-1" {
		t.Fatalf("entry %+v", entry)
	}
	if auditCount(t, store, "admin.credit_adjust") != 1 {
		t.Fatal("the grant has no audit row")
	}

	if _, err := store.db.Exec(`CREATE FUNCTION refuse_credit_audit() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'audit is down'; END $$ LANGUAGE plpgsql`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER refuse_credit_audit BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action = 'admin.credit_refused') EXECUTE FUNCTION refuse_credit_audit()`); err != nil {
		t.Fatal(err)
	}
	_, err := store.AdjustCredit(ctx, credits.Adjustment{OrgID: "org-a", Amount: 700, Actor: "admin-1", Reason: "this one must roll back", IdempotencyKey: "k-2",
		Audit: credits.AuditRecord{UserID: "admin-1", Action: "admin.credit_refused", Resource: "org"}}, creditsNow)
	if err == nil {
		t.Fatal("a failed audit insert must fail the grant")
	}
	if balance := creditInvariant(t, store, "org-a"); balance != 2500 {
		t.Fatalf("balance %d after a rolled-back grant, want 2500", balance)
	}
	var entries int
	_ = store.db.QueryRow(`SELECT count(*) FROM credit_entries WHERE idempotency_key = 'admin:org-a:k-2'`).Scan(&entries)
	if entries != 0 {
		t.Fatal("the ledger entry survived the failed audit")
	}
}

func TestAdminAdjustReplayAndConflict(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	first := fundOrg(t, store, "org-r", "same", 900, nil)
	again, err := store.AdjustCredit(ctx, credits.Adjustment{OrgID: "org-r", Amount: 900, Actor: "admin-1", Reason: "seed credit for a test", IdempotencyKey: "same",
		Audit: credits.AuditRecord{UserID: "admin-1", Action: "admin.credit_adjust", Resource: "org"}}, creditsNow)
	if err != nil || again.ID != first.ID {
		t.Fatalf("a replay returns the first entry: %+v %v", again, err)
	}
	if auditCount(t, store, "admin.credit_adjust") != 1 {
		t.Fatal("a replay wrote a second audit row")
	}
	_, err = store.AdjustCredit(ctx, credits.Adjustment{OrgID: "org-r", Amount: 901, Actor: "admin-1", Reason: "different amount", IdempotencyKey: "same",
		Audit: credits.AuditRecord{UserID: "admin-1", Action: "admin.credit_adjust", Resource: "org"}}, creditsNow)
	if !errors.Is(err, credits.ErrIdempotencyConflict) {
		t.Fatalf("the same key with another amount: %v", err)
	}
}

func TestNegativeAdjustmentMapsToLotsAndRefusesToGoBelowZero(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	fundOrg(t, store, "org-n", "a", 100, nil)
	fundOrg(t, store, "org-n", "b", 100, inHours(24))
	entry, err := store.AdjustCredit(ctx, negativeAdjustment("org-n", 150, "claw"), creditsNow)
	if err != nil || entry.Amount != -150 || entry.Kind != credits.KindAdjustAdmin {
		t.Fatalf("%+v %v", entry, err)
	}
	if byExpiry := lotRemainingByExpiry(t, store, "org-n"); byExpiry[24] != 0 || byExpiry[-1] != 50 {
		t.Fatalf("the soonest-expiring lot is drained first: %v", byExpiry)
	}
	creditInvariant(t, store, "org-n")
	_, err = store.AdjustCredit(ctx, negativeAdjustment("org-n", 51, "too-much"), creditsNow)
	if !errors.Is(err, credits.ErrInsufficientBalance) {
		t.Fatalf("an adjustment below zero: %v", err)
	}
	if balance := creditInvariant(t, store, "org-n"); balance != 50 {
		t.Fatalf("balance %d, want 50", balance)
	}
}

func TestAdjustRefusesAnOrgThatDoesNotExistWhenAsked(t *testing.T) {
	store := testStore(t)
	if ok, err := store.CreditOrgExists(context.Background(), "org-ghost"); err != nil || ok {
		t.Fatalf("a missing org: %v %v", ok, err)
	}
}

func TestStatementCoversOneUTCMonth(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	november := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	adjustForTest(t, store, "org-s", 1000, "nov", nil, november)
	if _, err := store.DebitCredit(ctx, credits.DebitRequest{OrgID: "org-s", Amount: 100, PeriodStart: creditsNow, PeriodEnd: creditsNow.Add(time.Hour), IdempotencyKey: "u-1"}); err != nil {
		t.Fatal(err)
	}
	statement, err := store.CreditStatement(ctx, "org-s", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if statement.Month != "2026-12" || statement.Opening != 1000 || statement.Closing != 900 || len(statement.Entries) != 1 || statement.Entries[0].Amount != -100 {
		t.Fatalf("december: %+v", statement)
	}
	statement, _ = store.CreditStatement(ctx, "org-s", november)
	if statement.Opening != 0 || statement.Closing != 1000 || len(statement.Entries) != 1 {
		t.Fatalf("november: %+v", statement)
	}
}

func TestAdminReplayWithAnotherReasonOrExpiryIsAConflict(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	base := credits.Adjustment{OrgID: "org-c", Amount: 900, Actor: "admin-1", Reason: "the first reason given", IdempotencyKey: "same-key", ExpiresAt: inHours(48),
		Audit: credits.AuditRecord{UserID: "admin-1", Action: "admin.credit_adjust", Resource: "org"}}
	if _, err := store.AdjustCredit(ctx, base, creditsNow); err != nil {
		t.Fatal(err)
	}
	same := base
	if _, err := store.AdjustCredit(ctx, same, creditsNow); err != nil {
		t.Fatalf("an identical replay is fine: %v", err)
	}
	otherReason := base
	otherReason.Reason = "a different reason entirely"
	if _, err := store.AdjustCredit(ctx, otherReason, creditsNow); !errors.Is(err, credits.ErrIdempotencyConflict) {
		t.Fatalf("another reason: %v", err)
	}
	otherExpiry := base
	otherExpiry.ExpiresAt = inHours(72)
	if _, err := store.AdjustCredit(ctx, otherExpiry, creditsNow); !errors.Is(err, credits.ErrIdempotencyConflict) {
		t.Fatalf("another expiry: %v", err)
	}
	noExpiry := base
	noExpiry.ExpiresAt = nil
	if _, err := store.AdjustCredit(ctx, noExpiry, creditsNow); !errors.Is(err, credits.ErrIdempotencyConflict) {
		t.Fatalf("no expiry: %v", err)
	}
}
