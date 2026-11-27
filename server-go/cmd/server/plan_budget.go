package main

import (
	"context"
	"log"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// planBudgetGate is the gate every step that grows an org's services passes
// (EXC-901). It checks nothing on a self-hosted install, which is not metered,
// or while FEATURE_PLANBUDGET is off.
func planBudgetGate(cfg config.AppConfig) *planbudget.Gate {
	return planbudget.NewGate(func(ctx context.Context) bool { return planBudgetOn(ctx, cfg) }, cfg.PlanBudget)
}

// planBudgetOn is the one test of whether the plan budget applies: the flag is
// on and this is the cloud. A self-hosted install is never metered.
func planBudgetOn(ctx context.Context, cfg config.AppConfig) bool {
	return cfg.IsCloud() && features.Enabled(ctx, cfg.Features(), features.PlanBudget)
}

// wirePlanBudget hands the gate to the stores and records what every service
// already holds. The backfill only writes the platform's own rows: no cluster
// or container is read, resized or restarted, so it is safe with the flag off
// and runs every boot.
func wirePlanBudget(cfg config.AppConfig, sqlStore storage.PlatformStore) *planbudget.Gate {
	gate := planBudgetGate(cfg)
	apphost.SetDefaultPlanBudget(gate)
	pg, ok := sqlStore.(*pgstore.Store)
	if !ok {
		return gate
	}
	pg.SetPlanBudget(gate)
	ctx := context.Background()
	recorded, failed, err := pg.BackfillReservations(ctx)
	if err != nil {
		failBackfill(cfg, "database sizes", err)
	}
	for _, one := range failed {
		log.Printf("ERROR: plan budget: could not record the size of %s, so its org's growth is refused until it is fixed: %v", one.ProjectID, one.Err)
	}
	if recorded > 0 {
		log.Printf("plan budget: recorded the size of %d databases", recorded)
	}
	reportOverBudget(ctx, pg)
	return gate
}

// failBackfill stops the boot when the budget is on and its numbers could not
// be recorded, since admitting against a partial sum would let usage outrun the
// plan; with the budget off it only says so.
func failBackfill(cfg config.AppConfig, what string, err error) {
	if planBudgetOn(context.Background(), cfg) {
		log.Fatalf("plan budget: record the %s: %v", what, err)
	}
	log.Printf("WARN: plan budget: record the %s: %v", what, err)
}

// planProjectCap is the plan budget's project count per plan, used only while
// the budget is on. Free holds one project, the owner's.
func planProjectCap(cfg config.AppConfig) service.ProjectCap {
	return func(ctx context.Context, tier domain.TierType) (int, bool) {
		if !planBudgetOn(ctx, cfg) {
			return 0, false
		}
		switch tier {
		case domain.Free:
			return cfg.PlanBudget.Free.MaxProjects, true
		case domain.Standard:
			return cfg.PlanBudget.Standard.MaxProjects, true
		}
		return 0, false
	}
}

// recordFunctionRuntimes marks the projects that already run functions, so their
// runtimes count against the plan from the first boot with the budget on.
func recordFunctionRuntimes(cfg config.AppConfig, pg *pgstore.Store, functions edgefn.Store) {
	lister, ok := functions.(interface{ ProjectIDs() ([]string, error) })
	if !ok {
		return
	}
	ids, err := lister.ProjectIDs()
	if err == nil {
		err = pg.MarkFunctionsRuntimes(context.Background(), ids)
	}
	if err != nil {
		failBackfill(cfg, "functions runtimes", err)
	}
}

// newPlanBudgetHandler serves the plan budget to an org's members; nil when the store cannot report it.
func newPlanBudgetHandler(sqlStore storage.PlatformStore) *handler.PlanBudgetHandler {
	source, ok := sqlStore.(handler.PlanBudgetSource)
	if !ok {
		return nil
	}
	return handler.NewPlanBudgetHandler(source, sqlStore)
}

// mountPlanBudget adds the budget read to the org routes, behind the planbudget flag.
func mountPlanBudget(d *handlerDeps) {
	if d.planBudgetHandler != nil && d.orgHandler != nil {
		d.orgHandler.SetPlanBudget(d.planBudgetHandler, features.Require(d.features, features.PlanBudget))
	}
}

// reportOverBudget names every org already over its plan from what is recorded.
// It is a report, not a refusal: such an org keeps what it runs and cannot grow.
// It reads records only; whether a cluster really runs at its recorded size is not checked here.
func reportOverBudget(ctx context.Context, pg *pgstore.Store) {
	over, err := pg.OrgsOverBudget(ctx)
	if err != nil {
		log.Printf("WARN: plan budget: check which orgs are over their plan: %v", err)
		return
	}
	for _, org := range over {
		log.Printf("WARN: plan budget: org %s holds %d mCPU, %d MiB, %d GiB, %d services against a plan of %d mCPU, %d MiB, %d GiB, %d services",
			org.OrgID, org.Used.CPUMilli, org.Used.MemoryBytes>>20, org.Used.DiskBytes>>30, org.Used.Services,
			org.Budget.CPUMilli, org.Budget.MemoryBytes>>20, org.Budget.DiskBytes>>30, org.Budget.Services)
	}
}
