package credits

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type recordingLedger struct {
	Ledger
	orgs             map[string]bool
	adjustments      []Adjustment
	terms            map[string]string
	grants           []string
	waitlist         []string
	pending          []string
	outcomes         map[string]GrantOutcome
	expired          int
	requireWatermark bool
}

func (l *recordingLedger) CreditOrgExists(_ context.Context, orgID string) (bool, error) {
	return l.orgs[orgID], nil
}

func (l *recordingLedger) AdjustCredit(_ context.Context, adjustment Adjustment, _ time.Time) (Entry, error) {
	l.adjustments = append(l.adjustments, adjustment)
	return Entry{ID: 1, OrgID: adjustment.OrgID, Amount: adjustment.Amount}, nil
}

func (l *recordingLedger) AcceptCreditTerms(_ context.Context, userID, version string, _ time.Time) error {
	l.terms[userID] = version
	return nil
}

func (l *recordingLedger) GrantCreditTrial(_ context.Context, userID string, _ Rules, _ time.Time) (GrantResult, error) {
	l.grants = append(l.grants, userID)
	return GrantResult{Outcome: l.outcomes[userID]}, nil
}

func (l *recordingLedger) ExpireCreditLots(_ context.Context, _ time.Time, requireWatermark bool) (int, error) {
	l.expired++
	l.requireWatermark = requireWatermark
	return 0, nil
}

func (l *recordingLedger) PendingCreditTrialUsers(context.Context, string, int) ([]string, error) {
	return l.pending, nil
}

func (l *recordingLedger) CreditWaitlist(context.Context, int) ([]string, error) {
	return l.waitlist, nil
}

var testRules = Rules{TrialAmount: FromDollars(300), TrialDays: 90, TrialCap: 40, AdminCap: FromDollars(1000), TermsVersion: "terms-1"}

var serviceNow = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

func newTestService(ledger *recordingLedger) *Service {
	return NewService(ledger, testRules, func() time.Time { return serviceNow })
}

func goodChange() AdminChange {
	return AdminChange{OrgID: "org-1", ActorID: "admin-1", AmountMicros: FromDollars(50), Reason: "goodwill after the outage", IdempotencyKey: "key-1"}
}

func TestAdjustAcceptsAGoodChangeAndBuildsTheAuditRecord(t *testing.T) {
	ledger := &recordingLedger{orgs: map[string]bool{"org-1": true}}
	if _, err := newTestService(ledger).Adjust(context.Background(), goodChange()); err != nil {
		t.Fatal(err)
	}
	audit := ledger.adjustments[0].Audit
	if audit.Action != "admin.credit_adjust" || audit.UserID != "admin-1" || audit.ResourceID != "org-1" || !strings.Contains(audit.Details, "goodwill after the outage") {
		t.Fatalf("audit %+v", audit)
	}
}

func TestAdjustRefusals(t *testing.T) {
	past := serviceNow.Add(-time.Hour)
	cases := map[string]struct {
		change func(*AdminChange)
		want   error
	}{
		"reason too short":   {func(c *AdminChange) { c.Reason = "short" }, ErrReasonLength},
		"reason blank":       {func(c *AdminChange) { c.Reason = "         " }, ErrReasonLength},
		"reason too long":    {func(c *AdminChange) { c.Reason = strings.Repeat("x", 501) }, ErrReasonLength},
		"zero amount":        {func(c *AdminChange) { c.AmountMicros = 0 }, ErrInvalidAmount},
		"grant above cap":    {func(c *AdminChange) { c.AmountMicros = FromDollars(1000) + 1 }, ErrAboveCap},
		"negative above cap": {func(c *AdminChange) { c.AmountMicros = -FromDollars(1000) - 1 }, ErrAboveCap},
		"no key":             {func(c *AdminChange) { c.IdempotencyKey = "" }, ErrMissingKey},
		"expiry in the past": {func(c *AdminChange) { c.ExpiresAt = &past }, ErrPastExpiry},
		"unknown org":        {func(c *AdminChange) { c.OrgID = "org-ghost" }, ErrOrgNotFound},
	}
	for name, tc := range cases {
		ledger := &recordingLedger{orgs: map[string]bool{"org-1": true}}
		change := goodChange()
		tc.change(&change)
		if _, err := newTestService(ledger).Adjust(context.Background(), change); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
		if len(ledger.adjustments) != 0 {
			t.Errorf("%s: a refused change reached the ledger", name)
		}
	}
}

func TestAdjustAllowsExactlyTheCap(t *testing.T) {
	ledger := &recordingLedger{orgs: map[string]bool{"org-1": true}}
	change := goodChange()
	change.AmountMicros = FromDollars(1000)
	if _, err := newTestService(ledger).Adjust(context.Background(), change); err != nil {
		t.Fatalf("the cap itself is allowed: %v", err)
	}
	change.AmountMicros, change.IdempotencyKey = -FromDollars(1000), "key-2"
	if _, err := newTestService(ledger).Adjust(context.Background(), change); err != nil {
		t.Fatalf("a clawback of the cap is allowed: %v", err)
	}
}

func TestAcceptTermsTakesOnlyTheCurrentVersionAndThenTriesTheGrant(t *testing.T) {
	ledger := &recordingLedger{terms: map[string]string{}, outcomes: map[string]GrantOutcome{"u1": OutcomeGranted}}
	service := newTestService(ledger)
	if _, err := service.AcceptTerms(context.Background(), "u1", "old"); !errors.Is(err, ErrTermsVersion) {
		t.Fatalf("an old version: %v", err)
	}
	if len(ledger.terms) != 0 {
		t.Fatal("an old version was recorded")
	}
	result, err := service.AcceptTerms(context.Background(), "u1", "terms-1")
	if err != nil || result.Outcome != OutcomeGranted || ledger.terms["u1"] != "terms-1" || len(ledger.grants) != 1 {
		t.Fatalf("%+v %v %v", result, err, ledger.grants)
	}
}

func TestSweepExpiresGrantsPendingAndServesTheQueueUntilTheCapIsFull(t *testing.T) {
	ledger := &recordingLedger{
		pending: []string{"a", "b"}, waitlist: []string{"c", "d", "e"},
		outcomes: map[string]GrantOutcome{"a": OutcomeWaitlisted, "b": OutcomeWaitlisted, "c": OutcomeGranted, "d": OutcomeWaitlisted},
	}
	newTestService(ledger).Sweep(context.Background())
	if ledger.expired != 1 || strings.Join(ledger.grants, "") != "abcd" {
		t.Fatalf("expired %d grants %v: every new arrival is queued, then the queue stops at the first person who still must wait", ledger.expired, ledger.grants)
	}
}

func TestSweepExpiresByTheWatermarkOnceMeteringIsOn(t *testing.T) {
	for _, metered := range []bool{false, true} {
		ledger := &recordingLedger{}
		rules := testRules
		rules.Metered = metered
		NewService(ledger, rules, func() time.Time { return serviceNow }).Sweep(context.Background())
		if ledger.requireWatermark != metered {
			t.Errorf("metered=%v but the ledger was told requireWatermark=%v", metered, ledger.requireWatermark)
		}
	}
}
