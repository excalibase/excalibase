package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/creditzero"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
	"github.com/excalibase/provisioning-poc/internal/service"
)

type recordedApps struct {
	calls []string
	err   error
}

func (r *recordedApps) PauseAppForCredit(_ context.Context, projectID, appID string, admitted func() error) (*apphost.App, error) {
	r.calls = append(r.calls, "stop "+projectID+"/"+appID)
	if err := admitted(); err != nil {
		return nil, err
	}
	return nil, r.err
}

func (r *recordedApps) ResumeApp(_ context.Context, projectID, appID, actor string) (*apphost.App, error) {
	r.calls = append(r.calls, "resume "+projectID+"/"+appID+" by "+actor)
	return nil, r.err
}

type recordedDatabases struct {
	calls []string
	err   error
}

func (r *recordedDatabases) PauseIdle(_ context.Context, projectID, reason string, admitted func() error) error {
	r.calls = append(r.calls, "pause "+projectID+" for "+reason)
	if err := admitted(); err != nil {
		return err
	}
	return r.err
}

func (r *recordedDatabases) Resume(_ context.Context, projectID string) error {
	r.calls = append(r.calls, "resume "+projectID)
	return r.err
}

func TestCreditPausesGoThroughTheSameLifecycleAPersonUses(t *testing.T) {
	apps, dbs := &recordedApps{}, &recordedDatabases{}
	work := creditZeroWorkloads{apps: apps, databases: dbs}
	ctx := context.Background()
	admit := func() error { return nil }
	for _, step := range []error{
		work.Pause(ctx, creditzero.Paused{Kind: creditzero.KindApp, ProjectID: "p1", ServiceID: "a1"}, admit),
		work.Pause(ctx, creditzero.Paused{Kind: creditzero.KindDatabase, ProjectID: "p2", ServiceID: "p2"}, admit),
		work.Resume(ctx, creditzero.Paused{Kind: creditzero.KindApp, ProjectID: "p1", ServiceID: "a1"}),
		work.Resume(ctx, creditzero.Paused{Kind: creditzero.KindDatabase, ProjectID: "p2", ServiceID: "p2"}),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}
	if got := apps.calls; len(got) != 2 || got[0] != "stop p1/a1" || got[1] != "resume p1/a1 by "+creditZeroActor {
		t.Fatalf("apps: %v", got)
	}
	if got := dbs.calls; len(got) != 2 || got[0] != "pause p2 for "+domain.PauseReasonCredits || got[1] != "resume p2" {
		t.Fatalf("databases: %v", got)
	}
}

func noCheck() error { return nil }

func TestAServiceTheLifecycleRefusedAsNotRunningIsNotCreditsAndTheCheckReachesIt(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		kind string
		err  error
	}{
		"an app its owner stopped":    {creditzero.KindApp, service.ErrAppNotRunning},
		"a database no longer ACTIVE": {creditzero.KindDatabase, service.ErrNoLongerIdle},
	} {
		work := creditZeroWorkloads{apps: &recordedApps{err: tc.err}, databases: &recordedDatabases{err: tc.err}}
		if err := work.Pause(ctx, creditzero.Paused{Kind: tc.kind}, noCheck); !errors.Is(err, creditzero.ErrNotCredits) {
			t.Errorf("%s: a refusal must read as not credit's: %v", name, err)
		}
	}
	work := creditZeroWorkloads{apps: &recordedApps{}, databases: &recordedDatabases{}}
	refuse := func() error { return creditzero.ErrNoLongerDue }
	for _, kind := range []string{creditzero.KindApp, creditzero.KindDatabase} {
		if err := work.Pause(ctx, creditzero.Paused{Kind: kind}, refuse); !errors.Is(err, creditzero.ErrNoLongerDue) {
			t.Errorf("%s: the check just before the pause is the lifecycle's own admission: %v", kind, err)
		}
	}
}

func TestAKindWithNoLifecycleIsRefusedNotIgnored(t *testing.T) {
	work := creditZeroWorkloads{apps: &recordedApps{}, databases: &recordedDatabases{}}
	if err := work.Pause(context.Background(), creditzero.Paused{Kind: "functions"}, noCheck); err == nil {
		t.Fatal("pausing something this cannot pause must fail, not report success")
	}
	if err := (creditZeroWorkloads{}).Pause(context.Background(), creditzero.Paused{Kind: creditzero.KindDatabase}, noCheck); err == nil {
		t.Fatal("no database pauser (no pausers configured) is an error")
	}
}

func TestAWorkloadFailureReachesTheEnforcer(t *testing.T) {
	boom := errors.New("pods remain")
	work := creditZeroWorkloads{apps: &recordedApps{err: boom}, databases: &recordedDatabases{err: boom}}
	for _, kind := range []string{creditzero.KindApp, creditzero.KindDatabase} {
		if err := work.Pause(context.Background(), creditzero.Paused{Kind: kind}, noCheck); !errors.Is(err, boom) {
			t.Errorf("%s pause: %v", kind, err)
		}
		if err := work.Resume(context.Background(), creditzero.Paused{Kind: kind}); !errors.Is(err, boom) {
			t.Errorf("%s resume: %v", kind, err)
		}
	}
}

func zeroConfig(mode string, flags ...features.Feature) config.AppConfig {
	cfg := burnConfig(mode, flags...)
	cfg.CreditMeter.GraceDays = 7
	return cfg
}

func TestTheAllowanceTheEnforcerPausesDownToIsThePlanBudgetsOwn(t *testing.T) {
	cpu, memory, ok := creditZeroAllowance(zeroConfig("cloud"))("FREE")
	if !ok || cpu != 1000 || memory != 1024<<20 {
		t.Fatalf("%d %d %v", cpu, memory, ok)
	}
	if _, _, ok := creditZeroAllowance(zeroConfig("cloud"))("ENTERPRISE"); ok {
		t.Fatal("a plan with no budget has no allowance to fall back to")
	}
}

func TestTheTransitionAgeComesFromConfig(t *testing.T) {
	cfg := zeroConfig("cloud")
	cfg.CreditMeter.TransitionMaxAgeMinutes = 45
	if got := creditTransitionAge(cfg); got != 45*time.Minute {
		t.Fatal(got)
	}
}

func TestTheGraceComesFromConfig(t *testing.T) {
	cfg := zeroConfig("cloud")
	cfg.CreditMeter.GraceDays = 3
	if got := creditGrace(cfg); got != 3*24*time.Hour {
		t.Fatal(got)
	}
}

type stubHold struct{ held bool }

func (s stubHold) HeldForCredit(context.Context, string, string) (bool, error) { return s.held, nil }

func TestAnUnsuspendHonoursTheCreditHoldOnlyWhileMeteringIsOn(t *testing.T) {
	ctx := context.Background()
	on := creditHoldWhenMetered(zeroConfig("cloud", features.CreditBurn), stubHold{held: true})
	if held, err := on.HeldForCredit(ctx, "p", "a"); err != nil || !held {
		t.Fatalf("on: %v %v", held, err)
	}
	off := creditHoldWhenMetered(zeroConfig("cloud"), stubHold{held: true})
	if held, err := off.HeldForCredit(ctx, "p", "a"); err != nil || held {
		t.Fatalf("with metering off no app is held by credit: %v %v", held, err)
	}
}

type stubZero struct{}

func (stubZero) OrgZero(context.Context, string) (credits.ZeroInfo, error) {
	return credits.ZeroInfo{AtZero: true}, nil
}

func TestTheZeroSectionIsEmptyWhileMeteringIsOff(t *testing.T) {
	ctx := context.Background()
	off, _ := zeroSourceWhenMetered(zeroConfig("cloud"), stubZero{}).OrgZero(ctx, "o")
	on, _ := zeroSourceWhenMetered(zeroConfig("cloud", features.CreditBurn), stubZero{}).OrgZero(ctx, "o")
	if off.AtZero || !on.AtZero {
		t.Fatalf("off %+v on %+v", off, on)
	}
}

func TestTheGateReadsTheCreditOnlyWhileMeteringIsOn(t *testing.T) {
	reads := 0
	read := func(context.Context, planbudget.Querier, string) (bool, error) {
		reads++
		return true, nil
	}
	off, err := creditZeroCheck(zeroConfig("cloud"), read)(context.Background(), nil, "org-1")
	if err != nil || off || reads != 0 {
		t.Fatalf("metering off never reads the credit: %v %v %d", off, err, reads)
	}
	on, err := creditZeroCheck(zeroConfig("cloud", features.CreditBurn), read)(context.Background(), nil, "org-1")
	if err != nil || !on || reads != 1 {
		t.Fatalf("metering on asks: %v %v %d", on, err, reads)
	}
	boom := errors.New("db down")
	if _, err := creditZeroCheck(zeroConfig("cloud", features.CreditBurn), func(context.Context, planbudget.Querier, string) (bool, error) { return false, boom })(context.Background(), nil, "o"); !errors.Is(err, boom) {
		t.Fatalf("a failed read is not an answer: %v", err)
	}
}

func TestTheBudgetWiringGivesTheGateTheCreditCheck(t *testing.T) {
	gate := wirePlanBudget(zeroConfig("cloud", features.CreditBurn, features.PlanBudget, features.Credits), nil)
	if !gate.ChecksCredit() {
		t.Fatal("the gate must ask about credit, or an org at $0 keeps its whole budget")
	}
}

func TestNothingStartsOffTheCloudOrWithoutTheLedger(t *testing.T) {
	startCreditZero(zeroConfig("selfhosted", features.CreditBurn), nil, nil, nil)()
	startCreditZero(zeroConfig("cloud", features.CreditBurn), nil, nil, nil)()
}

func TestTheZeroRulesAreStartedAtBootAfterTheMeter(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "defer startCreditZero(cfg, sqlStore, deps, pauseSvc)()") {
		t.Error("main.go never starts the $0 rules")
	}
}
