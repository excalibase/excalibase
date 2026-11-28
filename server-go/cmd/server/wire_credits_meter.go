package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/creditmeter"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

const (
	creditMeterLockID int64 = 0x6372_6564_6d74_0866
	// usageSampleKeep is how long billed samples stay before they are pruned.
	usageSampleKeep = 14 * 24 * time.Hour
)

// creditBurnOn is the one test of whether usage is metered: the flag is on and
// this is the cloud. A self-hosted install is never metered.
func creditBurnOn(cfg config.AppConfig) bool {
	return cfg.IsCloud() && features.Enabled(context.Background(), cfg.Features(), features.CreditBurn)
}

// creditBurnBootCheck refuses to start metering without the ledger and the
// budget: burning credit without admission would let usage outrun the ceiling.
func creditBurnBootCheck(cfg config.AppConfig) error {
	if !creditBurnOn(cfg) {
		return nil
	}
	flags, ctx := cfg.Features(), context.Background()
	needs := []struct {
		feature features.Feature
		why     string
	}{
		{features.Credits, "there is no ledger to draw from"},
		{features.PlanBudget, "usage would outrun the ceiling it is billed against"},
	}
	for _, need := range needs {
		if !features.Enabled(ctx, flags, need.feature) {
			return fmt.Errorf("FEATURE_CREDITBURN needs FEATURE_%s on first: %s", strings.ToUpper(string(need.feature)), need.why)
		}
	}
	return nil
}

func creditPrices(cfg config.AppConfig) creditmeter.Prices {
	m := cfg.CreditMeter
	return creditmeter.Prices{
		Version: m.PriceVersion, VCPUMonth: credits.Micros(m.VCPUMonthMicros),
		RAMGiBMonth: credits.Micros(m.RAMGiBMonthMicros), DiskGiBMonth: credits.Micros(m.DiskGiBMonthMicros),
	}
}

// creditAllowance is what a plan is never charged for, read from the plan budget's
// table: one source for the ceiling and the allowance. ok is false for a plan the
// meter does not charge (ENTERPRISE, or one it does not know): it is skipped and
// counted, never charged at a zero allowance.
func creditAllowance(cfg config.AppConfig) func(tier string) (creditmeter.Allowance, bool) {
	return func(tier string) (creditmeter.Allowance, bool) {
		var plan config.PlanNumbers
		switch domain.TierType(strings.ToUpper(tier)) {
		case domain.Free:
			plan = cfg.PlanBudget.Free
		case domain.Standard:
			plan = cfg.PlanBudget.Standard
		default:
			return creditmeter.Allowance{}, false
		}
		return creditmeter.Allowance{
			CPUMilli: int64(plan.AllowanceCPUMilli), MemoryBytes: int64(plan.AllowanceMemoryMiB) << 20, DiskBytes: int64(plan.AllowanceDiskGiB) << 30,
		}, true
	}
}

// allowanceSetter is a store that stamps each sample with the plan's allowance.
type allowanceSetter interface {
	SetCreditAllowance(lookup func(tier string) (creditmeter.Allowance, bool))
}

// setCreditAllowance gives the store the plan allowances, while metering is on.
func setCreditAllowance(cfg config.AppConfig, store any) {
	if setter, ok := store.(allowanceSetter); ok && creditBurnOn(cfg) {
		setter.SetCreditAllowance(creditAllowance(cfg))
	}
}

// usageObserving is a store that can sample an org inside a step's transaction.
type usageObserving interface {
	ObserveOrgUsage(ctx context.Context, tx planbudget.Querier, orgID string)
}

// creditObserver is what the plan budget gate calls before each step, so a
// service paused or resized mid-slot still counts for the slot it ran in
// (R14). Nil while metering is off.
func creditObserver(cfg config.AppConfig, store any) planbudget.Observer {
	sampler, ok := store.(usageObserving)
	if !ok || !creditBurnOn(cfg) {
		return nil
	}
	return sampler.ObserveOrgUsage
}

// startCreditMeter samples usage every slot and bills each settled hour, on the
// leading replica only. Nothing runs while the flag is off.
func startCreditMeter(cfg config.AppConfig, sqlStore storage.PlatformStore, svc *credits.Service) func() {
	pg, ok := sqlStore.(*pgstore.Store)
	if !creditBurnOn(cfg) || !ok || svc == nil {
		return func() {
			// metering is off: nothing to sample or bill
		}
	}
	if err := pg.MarkMeteringEnabled(context.Background()); err != nil {
		log.Fatalf("credit meter: record when metering began: %v", err)
	}
	meter := creditmeter.NewMeter(pg, svc, creditPrices(cfg))
	sampler := creditmeter.NewSampler(pg)
	lock := standingLock(sqlStore, creditMeterLockID)
	runner := creditmeter.NewRunner(service.NewLeadership(lock), sampler.Pass, func(ctx context.Context) error {
		err := meter.Run(ctx)
		if _, pruneErr := pg.PruneUsageSamples(ctx, usageSampleKeep); pruneErr != nil {
			log.Printf("credit meter: prune samples: %v", pruneErr)
		}
		return err
	})
	log.Printf("credit meter: on (prices %s)", cfg.CreditMeter.PriceVersion)
	return runner.Start(context.Background())
}
