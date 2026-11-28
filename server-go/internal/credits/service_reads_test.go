package credits

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// readLedger answers the reads and pass-throughs the service adds no rules to.
type readLedger struct {
	Ledger
	account       Account
	accountErr    error
	entries       []Entry
	entriesLimit  int
	entriesBefore int64
	trial         TrialState
	trialErr      error
	acceptedTerms string
	termsErr      error
	debit         DebitResult
	watermark     time.Time
	advancedTo    time.Time
	expireErr     error
	pendingErr    error
	waitlistErr   error
	pending       []string
	waitlist      []string
	outcomes      map[string]GrantOutcome
	grantErr      map[string]error
	granted       []string
	burn          UsageBurn
	burnErr       error
	burnFrom      time.Time
}

func (l *readLedger) CreditAccount(context.Context, string) (Account, error) {
	return l.account, l.accountErr
}

func (l *readLedger) CreditEntries(_ context.Context, _ string, before int64, limit int) ([]Entry, error) {
	l.entriesBefore, l.entriesLimit = before, limit
	return l.entries, nil
}

func (l *readLedger) CreditUsageBurn(_ context.Context, _ string, from time.Time) (UsageBurn, error) {
	l.burnFrom = from
	return l.burn, l.burnErr
}

func (l *readLedger) CreditTrialState(context.Context, string) (TrialState, error) {
	return l.trial, l.trialErr
}

func (l *readLedger) CreditTermsVersion(context.Context, string) (string, error) {
	return l.acceptedTerms, l.termsErr
}

func (l *readLedger) CreditStatement(_ context.Context, orgID string, month time.Time) (Statement, error) {
	return Statement{Month: month.Format("2006-01")}, nil
}

func (l *readLedger) DebitCredit(context.Context, DebitRequest) (DebitResult, error) {
	return l.debit, nil
}

func (l *readLedger) CreditMeterWatermark(context.Context, string) (time.Time, bool, error) {
	return l.watermark, !l.watermark.IsZero(), nil
}

func (l *readLedger) AdvanceCreditMeterWatermark(_ context.Context, _ string, to time.Time) error {
	l.advancedTo = to
	return nil
}

func (l *readLedger) ExpireCreditLots(context.Context, time.Time, bool) (int, error) {
	return 0, l.expireErr
}

func (l *readLedger) PendingCreditTrialUsers(context.Context, string, int) ([]string, error) {
	return l.pending, l.pendingErr
}

func (l *readLedger) CreditWaitlist(context.Context, int) ([]string, error) {
	return l.waitlist, l.waitlistErr
}

func (l *readLedger) GrantCreditTrial(_ context.Context, userID string, _ Rules, _ time.Time) (GrantResult, error) {
	l.granted = append(l.granted, userID)
	return GrantResult{Outcome: l.outcomes[userID]}, l.grantErr[userID]
}

func newReadService(ledger *readLedger) *Service {
	return NewService(ledger, testRules, func() time.Time { return serviceNow })
}

func TestTheOverviewCarriesTheAccountTheNextExpiryAndTheTrial(t *testing.T) {
	soon, later := serviceNow.Add(48*time.Hour), serviceNow.Add(96*time.Hour)
	ledger := &readLedger{
		account: Account{OrgID: "o1", Balance: 5, Lots: []Lot{{ID: 1, ExpiresAt: &later}, {ID: 2}, {ID: 3, ExpiresAt: &soon}}},
		entries: []Entry{{ID: 9}},
		trial:   TrialState{State: "granted"},
	}
	overview, err := newReadService(ledger).Overview(context.Background(), "o1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if overview.NextExpiry == nil || !overview.NextExpiry.Equal(soon) {
		t.Fatalf("the soonest expiry wins and a lot with none is skipped: %v", overview.NextExpiry)
	}
	if overview.Trial.State != "granted" || overview.TermsVersion != "terms-1" || overview.BilledIn != "UTC" || len(overview.Entries) != 1 {
		t.Fatalf("%+v", overview)
	}
	if ledger.entriesLimit != recentEntryCount || ledger.entriesBefore != 0 {
		t.Fatalf("the overview opens with the newest %d lines: %d %d", recentEntryCount, ledger.entriesLimit, ledger.entriesBefore)
	}
}

func TestAnOrgWithNoDatedLotHasNoNextExpiry(t *testing.T) {
	overview, err := newReadService(&readLedger{account: Account{Lots: []Lot{{ID: 1}}}, trial: TrialState{State: "granted"}}).Overview(context.Background(), "o1", "u1")
	if err != nil || overview.NextExpiry != nil {
		t.Fatalf("%v %v", overview.NextExpiry, err)
	}
}

func TestAPersonWithNoGrantIsAskedForTermsOrIsPending(t *testing.T) {
	cases := map[string]string{"": "needs_terms", "old-terms": "needs_terms", "terms-1": "pending"}
	for accepted, want := range cases {
		overview, err := newReadService(&readLedger{trial: TrialState{State: "none"}, acceptedTerms: accepted}).Overview(context.Background(), "o1", "u1")
		if err != nil || overview.Trial.State != want {
			t.Errorf("accepted %q: %q %v, want %q", accepted, overview.Trial.State, err, want)
		}
		if overview.Trial.AmountMicro != testRules.TrialAmount || overview.Trial.Days != testRules.TrialDays {
			t.Errorf("the offer is the rules' amount and days: %+v", overview.Trial)
		}
	}
}

func TestAWaitlistedPersonKeepsTheirPlaceInTheOverview(t *testing.T) {
	overview, err := newReadService(&readLedger{trial: TrialState{State: "waitlisted", Position: 7}}).Overview(context.Background(), "o1", "u1")
	if err != nil || overview.Trial.State != "waitlisted" || overview.Trial.Position != 7 {
		t.Fatalf("%+v %v", overview.Trial, err)
	}
}

func TestEveryReadFailureReachesTheCaller(t *testing.T) {
	boom := errors.New("boom")
	for name, ledger := range map[string]*readLedger{
		"account": {accountErr: boom},
		"trial":   {trialErr: boom},
		"terms":   {trial: TrialState{State: "none"}, termsErr: boom},
	} {
		if _, err := newReadService(ledger).Overview(context.Background(), "o1", "u1"); !errors.Is(err, boom) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEntriesFallBackToTheDefaultPageForAnOutOfRangeLimit(t *testing.T) {
	for limit, want := range map[int]int{0: recentEntryCount, -3: recentEntryCount, maxEntriesPage + 1: recentEntryCount, 7: 7, maxEntriesPage: maxEntriesPage} {
		ledger := &readLedger{}
		if _, err := newReadService(ledger).Entries(context.Background(), "o1", 42, limit); err != nil {
			t.Fatal(err)
		}
		if ledger.entriesLimit != want || ledger.entriesBefore != 42 {
			t.Errorf("limit %d asked for %d (before %d), want %d", limit, ledger.entriesLimit, ledger.entriesBefore, want)
		}
	}
}

func TestStatementDebitAndMeterPassThroughToTheLedger(t *testing.T) {
	mark := serviceNow.Add(-time.Hour)
	ledger := &readLedger{debit: DebitResult{Taken: 5}, watermark: mark}
	svc := newReadService(ledger)
	ctx := context.Background()
	if statement, err := svc.Statement(ctx, "o1", serviceNow); err != nil || statement.Month != "2026-12" {
		t.Fatalf("%+v %v", statement, err)
	}
	if result, err := svc.Debit(ctx, DebitRequest{OrgID: "o1"}); err != nil || result.Taken != 5 {
		t.Fatalf("%+v %v", result, err)
	}
	if got, ok, err := svc.MeterWatermark(ctx, "o1"); err != nil || !ok || !got.Equal(mark) {
		t.Fatalf("%v %v %v", got, ok, err)
	}
	if err := svc.AdvanceMeterWatermark(ctx, "o1", serviceNow); err != nil || !ledger.advancedTo.Equal(serviceNow) {
		t.Fatalf("%v %v", ledger.advancedTo, err)
	}
	if svc.Rules().TermsVersion != "terms-1" {
		t.Fatal("the rules are the ones the service was built with")
	}
}

func TestASweepKeepsGoingWhenEachStepFails(t *testing.T) {
	boom := errors.New("boom")
	ledger := &readLedger{expireErr: boom, pendingErr: boom, waitlistErr: boom}
	newReadService(ledger).Sweep(context.Background())
	if len(ledger.granted) != 0 {
		t.Fatalf("nothing to grant when every read failed: %v", ledger.granted)
	}
}

func TestAGrantFailureDoesNotStopTheSweepAndTheQueueStopsAtTheFirstWaiter(t *testing.T) {
	boom := errors.New("boom")
	ledger := &readLedger{
		pending:  []string{"a", "b"},
		waitlist: []string{"w1", "w2", "w3"},
		grantErr: map[string]error{"a": boom},
		outcomes: map[string]GrantOutcome{"b": OutcomeGranted, "w1": OutcomeGranted, "w2": OutcomeWaitlisted},
	}
	newReadService(ledger).Sweep(context.Background())
	want := []string{"a", "b", "w1", "w2"}
	if len(ledger.granted) != len(want) {
		t.Fatalf("a failed grant is skipped, new arrivals all try, and the queue stops at the first who still waits: %v", ledger.granted)
	}
	for i, id := range want {
		if ledger.granted[i] != id {
			t.Fatalf("%v, want %v", ledger.granted, want)
		}
	}
}

type fixedLeader struct {
	leading bool
	err     error
	asked   atomic.Int32
}

func (l *fixedLeader) IsLeader(context.Context) (bool, error) {
	l.asked.Add(1)
	return l.leading, l.err
}

func TestOnlyTheLeadingReplicaSweeps(t *testing.T) {
	for name, c := range map[string]struct {
		leader *fixedLeader
		sweeps bool
	}{
		"leader":     {&fixedLeader{leading: true}, true},
		"follower":   {&fixedLeader{}, false},
		"unreadable": {&fixedLeader{err: errors.New("down")}, false},
	} {
		ledger := &readLedger{pending: []string{"p"}, outcomes: map[string]GrantOutcome{"p": OutcomeGranted}}
		newReadService(ledger).sweepIfLeader(context.Background(), c.leader)
		if (len(ledger.granted) > 0) != c.sweeps {
			t.Errorf("%s: granted %v", name, ledger.granted)
		}
	}
}

func TestTheSweeperRunsAtOnceAndStopsWhenAsked(t *testing.T) {
	leader := &fixedLeader{leading: true}
	stop := newReadService(&readLedger{}).StartSweeper(context.Background(), leader, time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for leader.asked.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	if leader.asked.Load() < 3 {
		t.Fatalf("the sweeper ticks: %d", leader.asked.Load())
	}
	settled := leader.asked.Load()
	time.Sleep(20 * time.Millisecond)
	if leader.asked.Load() > settled+1 {
		t.Fatalf("it kept running after stop: %d then %d", settled, leader.asked.Load())
	}
}

func TestTheOverviewCarriesTheBurnAndWhenTheCreditRunsOut(t *testing.T) {
	now := time.Date(2026, 12, 8, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)
	ledger := &readLedger{
		account: Account{Balance: 10_000_000},
		trial:   TrialState{State: "granted"},
		burn:    UsageBurn{Days: []DayBurn{{Day: now.Truncate(24 * time.Hour), Micros: 2_000_000}}, Since: &since},
	}
	service := NewService(ledger, Rules{TermsVersion: "v1"}, func() time.Time { return now })
	overview, err := service.Overview(context.Background(), "o1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if overview.Burn.PerDay != 2_000_000 || overview.Burn.RunOutAt == nil || !overview.Burn.RunOutAt.Equal(now.Add(5*24*time.Hour)) {
		t.Fatalf("burn %+v", overview.Burn)
	}
	if !ledger.burnFrom.Equal(BurnWindowStart(now)) {
		t.Fatalf("the ledger is asked for the seven-day window, got %v", ledger.burnFrom)
	}
}

func TestAFailureReadingTheBurnFailsTheOverview(t *testing.T) {
	ledger := &readLedger{trial: TrialState{State: "granted"}, burnErr: errors.New("read failed")}
	if _, err := newReadService(ledger).Overview(context.Background(), "o1", "u1"); err == nil {
		t.Fatal("a half page that hides a failed read would show a wrong run-out date")
	}
}

func TestTheOverviewSaysWhetherUsageIsBilledYet(t *testing.T) {
	for _, metered := range []bool{false, true} {
		service := NewService(&readLedger{trial: TrialState{State: "granted"}}, Rules{Metered: metered}, nil)
		overview, err := service.Overview(context.Background(), "o1", "u1")
		if err != nil || overview.Metering != metered {
			t.Fatalf("metered %v: overview says %v (%v)", metered, overview.Metering, err)
		}
	}
}
