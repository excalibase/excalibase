package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/creditzero"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

const (
	creditZeroLockID   int64 = 0x6372_6564_7a65_0866
	creditZeroInterval       = time.Minute
	// creditZeroActor is who a credit resume is recorded as.
	creditZeroActor = "system:credits"
)

// creditZeroApps and creditZeroDatabases are the lifecycle a person's own pause and resume use.
type creditZeroApps interface {
	StopApp(ctx context.Context, projectID, appID string) (*apphost.App, error)
	ResumeApp(ctx context.Context, projectID, appID, actor string) (*apphost.App, error)
}

type creditZeroDatabases interface {
	Pause(ctx context.Context, projectID, reason string) error
	Resume(ctx context.Context, projectID string) error
}

// creditZeroWorkloads pauses and resumes what the credit holds through the same
// services a person's own pause and resume go through, so nothing here reaches
// around the lifecycle lease or the plan budget.
type creditZeroWorkloads struct {
	apps      creditZeroApps
	databases creditZeroDatabases
}

var errNoCreditPause = errors.New("this kind of service cannot be paused for credit")

func (w creditZeroWorkloads) Pause(ctx context.Context, row creditzero.Paused) error {
	switch row.Kind {
	case creditzero.KindApp:
		if w.apps == nil {
			return errNoCreditPause
		}
		_, err := w.apps.StopApp(ctx, row.ProjectID, row.ServiceID)
		return err
	case creditzero.KindDatabase:
		if w.databases == nil {
			return errNoCreditPause
		}
		return w.databases.Pause(ctx, row.ProjectID, domain.PauseReasonCredits)
	}
	return fmt.Errorf("%w: %s", errNoCreditPause, row.Kind)
}

func (w creditZeroWorkloads) Resume(ctx context.Context, row creditzero.Paused) error {
	switch row.Kind {
	case creditzero.KindApp:
		if w.apps == nil {
			return errNoCreditPause
		}
		_, err := w.apps.ResumeApp(ctx, row.ProjectID, row.ServiceID, creditZeroActor)
		return err
	case creditzero.KindDatabase:
		if w.databases == nil {
			return errNoCreditPause
		}
		return w.databases.Resume(ctx, row.ProjectID)
	}
	return fmt.Errorf("%w: %s", errNoCreditPause, row.Kind)
}

// creditZeroAllowance is the CPU and memory a plan is never charged for: the plan budget's own.
func creditZeroAllowance(cfg config.AppConfig) creditzero.Allowance {
	allowance := creditAllowance(cfg)
	return func(tier string) (int64, int64, bool) {
		size, ok := allowance(tier)
		return size.CPUMilli, size.MemoryBytes, ok
	}
}

func creditGrace(cfg config.AppConfig) time.Duration {
	return time.Duration(cfg.CreditMeter.GraceDays) * 24 * time.Hour
}

// creditZeroCheck tells the plan budget gate whether an org's credit is used up, while credit is metered.
func creditZeroCheck(cfg config.AppConfig, read planbudget.ZeroCheck) planbudget.ZeroCheck {
	return func(ctx context.Context, q planbudget.Querier, orgID string) (bool, error) {
		if !creditBurnOn(cfg) {
			return false, nil
		}
		return read(ctx, q, orgID)
	}
}

// setCreditZeroCheck makes the gate drop an org whose credit is used up to its free allowance.
func setCreditZeroCheck(cfg config.AppConfig, gate *planbudget.Gate) {
	gate.SetZeroCheck(creditZeroCheck(cfg, planbudget.CreditUsedUp))
}

// creditHoldWhenMetered keeps an unsuspend from starting an app credit holds, while credit is metered.
func creditHoldWhenMetered(cfg config.AppConfig, hold adminconsole.CreditHold) adminconsole.CreditHold {
	return meteredHold{cfg: cfg, hold: hold}
}

type meteredHold struct {
	cfg  config.AppConfig
	hold adminconsole.CreditHold
}

func (m meteredHold) HeldForCredit(ctx context.Context, projectID, appID string) (bool, error) {
	if !creditBurnOn(m.cfg) {
		return false, nil
	}
	return m.hold.HeldForCredit(ctx, projectID, appID)
}

// zeroSourceWhenMetered answers an empty state while credit is not metered.
func zeroSourceWhenMetered(cfg config.AppConfig, source credits.ZeroSource) credits.ZeroSource {
	return meteredZero{cfg: cfg, source: source}
}

type meteredZero struct {
	cfg    config.AppConfig
	source credits.ZeroSource
}

func (m meteredZero) OrgZero(ctx context.Context, orgID string) (credits.ZeroInfo, error) {
	if !creditBurnOn(m.cfg) {
		return credits.ZeroInfo{}, nil
	}
	return m.source.OrgZero(ctx, orgID)
}

// startCreditZero runs the $0 rules (EXC-866 C.5) on the leading replica: after
// the grace period it pauses compute above the free allowance, and it resumes
// exactly what it paused when credit returns, a plan is upgraded or the
// creditburn flag is turned off. Creates and growth are refused by the plan
// budget gate, not here. It also hands Studio the state and the unsuspend its hold.
func startCreditZero(cfg config.AppConfig, sqlStore storage.PlatformStore, deps *handlerDeps, pauseSvc *service.PauseService) func() {
	pg, ok := sqlStore.(*pgstore.Store)
	if !cfg.IsCloud() || !ok || deps == nil || deps.credits == nil {
		return func() {
			// nothing is metered here: no credit to run out
		}
	}
	zero := pgstore.NewCreditZeroStore(pg)
	deps.credits.SetZero(zeroSourceWhenMetered(cfg, zero), creditGrace(cfg))
	if deps.suspensions != nil {
		deps.suspensions.SetCreditHold(creditHoldWhenMetered(cfg, zero))
	}
	work := creditZeroWorkloads{}
	if deps.appDeploySvc != nil {
		work.apps = deps.appDeploySvc
	}
	if pauseSvc != nil {
		work.databases = databasePauser{pauseSvc}
	}
	enforcer := creditzero.New(zero, work, creditZeroAllowance(cfg), creditGrace(cfg))
	lock := standingLock(sqlStore, creditZeroLockID)
	log.Printf("credit zero rules: grace %d days, checked every %s while creditburn is on", cfg.CreditMeter.GraceDays, creditZeroInterval)
	return enforcer.Start(context.Background(), service.NewLeadership(lock), creditZeroInterval, func(context.Context) bool { return creditBurnOn(cfg) })
}

// databasePauser adapts the pause service to the two calls credit needs.
type databasePauser struct{ pauses *service.PauseService }

func (d databasePauser) Pause(ctx context.Context, projectID, reason string) error {
	return d.pauses.Pause(ctx, projectID, reason)
}

func (d databasePauser) Resume(ctx context.Context, projectID string) error {
	return d.pauses.Resume(ctx, projectID)
}

var _ creditZeroApps = (*service.AppDeployService)(nil)
