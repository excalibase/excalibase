package credits

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeZero struct {
	info ZeroInfo
	err  error
}

func (f fakeZero) OrgZero(context.Context, string) (ZeroInfo, error) { return f.info, f.err }

func zeroService(ledger *readLedger, source ZeroSource) *Service {
	service := NewService(ledger, Rules{TermsVersion: "v1", TrialAmount: FromDollars(300)}, func() time.Time { return serviceNow })
	if source != nil {
		service.SetZero(source, 7*24*time.Hour)
	}
	return service
}

func TestWithoutAZeroSourceTheOverviewHasNoZeroSection(t *testing.T) {
	overview, err := zeroService(&readLedger{trial: TrialState{State: "granted"}}, nil).Overview(context.Background(), "o1", "u1")
	if err != nil || overview.Zero != nil {
		t.Fatalf("metering off shows nothing: %+v %v", overview.Zero, err)
	}
}

func TestAnOrgAtZeroSeesWhenItsComputeAboveTheAllowanceWillPause(t *testing.T) {
	since := serviceNow.Add(-2 * 24 * time.Hour)
	source := fakeZero{info: ZeroInfo{AtZero: true, Since: &since, Paused: []PausedService{{Kind: "app", Name: "web"}}}}
	overview, err := zeroService(&readLedger{trial: TrialState{State: "granted"}}, source).Overview(context.Background(), "o1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	zero := overview.Zero
	if zero == nil || !zero.AtZero || zero.GraceEndsAt == nil || !zero.GraceEndsAt.Equal(since.Add(7*24*time.Hour)) || zero.GraceDays != 7 {
		t.Fatalf("the pause date is the day the credit ran out plus the grace: %+v", zero)
	}
	if len(zero.Paused) != 1 || zero.Paused[0].Name != "web" {
		t.Fatalf("%+v", zero.Paused)
	}
}

func TestAnOrgAtZeroWhoseGraceHasNotStartedYetIsGivenTheFullGrace(t *testing.T) {
	source := fakeZero{info: ZeroInfo{AtZero: true}}
	overview, err := zeroService(&readLedger{trial: TrialState{State: "granted"}}, source).Overview(context.Background(), "o1", "u1")
	if err != nil || overview.Zero.GraceEndsAt == nil || !overview.Zero.GraceEndsAt.Equal(serviceNow.Add(7*24*time.Hour)) {
		t.Fatalf("%+v %v", overview.Zero, err)
	}
}

func TestAnOrgWithCreditHasNoPauseDate(t *testing.T) {
	overview, err := zeroService(&readLedger{account: Account{Balance: FromDollars(200)}, trial: TrialState{State: "granted"}}, fakeZero{}).Overview(context.Background(), "o1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if overview.Zero.AtZero || overview.Zero.GraceEndsAt != nil || overview.Zero.LowCredit || overview.Zero.Paused == nil {
		t.Fatalf("an empty list, not null: %+v", overview.Zero)
	}
}

func TestLowCreditIsTwentyPercentOfTheSignUpCreditOrLessButNotZero(t *testing.T) {
	cases := map[string]struct {
		balance Micros
		low     bool
	}{
		"plenty":         {FromDollars(61), false},
		"exactly 20%":    {FromDollars(60), true},
		"a dollar left":  {FromDollars(1), true},
		"a micro-dollar": {1, true},
		"nothing":        {0, false},
	}
	for name, tc := range cases {
		overview, err := zeroService(&readLedger{account: Account{Balance: tc.balance}, trial: TrialState{State: "granted"}}, fakeZero{}).Overview(context.Background(), "o1", "u1")
		if err != nil || overview.Zero.LowCredit != tc.low {
			t.Errorf("%s: low=%v err=%v, want %v", name, overview.Zero.LowCredit, err, tc.low)
		}
	}
}

func TestAFailureReadingTheZeroStateFailsTheOverview(t *testing.T) {
	if _, err := zeroService(&readLedger{trial: TrialState{State: "granted"}}, fakeZero{err: errors.New("db down")}).Overview(context.Background(), "o1", "u1"); err == nil {
		t.Fatal("a page that hides a failed read would say the credit is fine")
	}
}
