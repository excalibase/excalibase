package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	creditsSweepLockID   int64 = 0x6372_6564_7377_0866
	creditsSweepInterval       = time.Minute
)

func creditsOn(cfg config.AppConfig) bool {
	return features.Enabled(context.Background(), cfg.Features(), features.Credits)
}

func creditRules(cfg config.AppConfig) credits.Rules {
	c := cfg.Credits
	return credits.Rules{
		TrialAmount: credits.FromDollars(int64(c.TrialDollars)), TrialDays: c.TrialDays, TrialCap: c.TrialCap,
		AdminCap: credits.FromDollars(int64(c.AdminGrantCapDollars)), TermsVersion: c.TermsVersion,
	}
}

// buildCreditsService is the credit rules over the platform store, or nil when
// the store keeps no ledger.
func buildCreditsService(cfg config.AppConfig, sqlStore storage.PlatformStore) *credits.Service {
	ledger, ok := sqlStore.(credits.Ledger)
	if !ok {
		return nil
	}
	return credits.NewService(ledger, creditRules(cfg), nil)
}

func newCreditsHandler(cfg config.AppConfig, svc *credits.Service, sqlStore storage.PlatformStore) *handler.CreditsHandler {
	if svc == nil {
		return nil
	}
	return handler.NewCreditsHandler(svc, sqlStore, cfg.Credits.TermsURL)
}

// startCreditsSweeper expires lots, grants the sign-up credit to whoever a
// direct call missed and serves the queue; nothing runs while the flag is off.
func startCreditsSweeper(cfg config.AppConfig, sqlStore storage.PlatformStore, svc *credits.Service) func() {
	if !creditsOn(cfg) || svc == nil {
		return func() {
			// credits are off: nothing to sweep
		}
	}
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() && sqlStore != nil {
		lock = standingLock(sqlStore, creditsSweepLockID)
	}
	log.Printf("credits: on (trial %d dollars, %d days, cap %d)", cfg.Credits.TrialDollars, cfg.Credits.TrialDays, cfg.Credits.TrialCap)
	return svc.StartSweeper(context.Background(), service.NewLeadership(lock), creditsSweepInterval)
}

// mountCredits adds the credit routes to the org handler and returns the gate
// the person routes use.
func mountCredits(d *handlerDeps) func(http.Handler) http.Handler {
	gate := features.Require(d.features, features.Credits)
	if d.creditsHandler != nil && d.orgHandler != nil {
		d.orgHandler.SetCredits(d.creditsHandler, gate)
	}
	return gate
}

// creditsBootCheck refuses to start with credits on while the limited trial's
// container gate (EXC-899) is off: credit must not fund containers for accounts
// nobody has verified. With credits off it asks nothing.
func creditsBootCheck(cfg config.AppConfig) error {
	flags := cfg.Features()
	ctx := context.Background()
	if features.Enabled(ctx, flags, features.Credits) && !features.Enabled(ctx, flags, features.LimitedTrial) {
		return errors.New("FEATURE_CREDITS is on but FEATURE_LIMITEDTRIAL is off: credit would fund containers for accounts nobody has verified; turn on the limited trial first")
	}
	return nil
}
