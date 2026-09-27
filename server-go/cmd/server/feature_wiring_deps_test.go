package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
)

// The boot check must ask the function handler whether it can open a project
// database, not whether an opener exists somewhere in main: a handler built
// without SetProjectDBFn applies no schema and syncs no cron, whatever main holds.
func TestFeatureWiringDeps_ProjectDBComesFromTheFunctionHandler(t *testing.T) {
	store := bootInstanceStore(t)
	opener := projectdb.NewOpener(store, bootVault{}, projectdb.Overrides{}, projectdb.PoolLimits{})
	defer opener.Close()
	unwired := handler.NewFunctionHandler(nil, nil, nil, store, nil, "")

	deps := featureWiringDeps(unwired, opener, nil)

	if deps.ProjectDB {
		t.Error("a function handler with no project database resolver was reported as wired")
	}
	if !deps.SchedulerProjects {
		t.Error("the opener lists servable projects; the scheduler's project list is wired")
	}
}

func TestFeatureWiringDeps_ProductionBuilderIsFullyWired(t *testing.T) {
	store := bootInstanceStore(t)
	opener := projectdb.NewOpener(store, bootVault{}, projectdb.Overrides{}, projectdb.PoolLimits{})
	defer opener.Close()
	cfg := config.AppConfig{StoragePath: t.TempDir(), ProvisionerMode: "docker"}
	fn := buildFunctionHandler(cfg, bootVault{}, store, nil, nil, opener)

	deps := featureWiringDeps(fn, opener, nil)

	if !deps.ProjectDB || !deps.SchedulerInvoker || !deps.SchedulerProjects || !deps.FunctionRuntime || !deps.CronLeader {
		t.Errorf("production function wiring reported incomplete: %+v", deps)
	}
	if deps.PauseService {
		t.Error("no pause service was built, so it must not be reported as wired")
	}
}

func TestFeatureWiringDeps_NothingBuilt(t *testing.T) {
	deps := featureWiringDeps(nil, nil, nil)
	if deps.ProjectDB || deps.SchedulerInvoker || deps.SchedulerProjects || deps.FunctionRuntime || deps.PauseService {
		t.Errorf("nothing built, yet something reported wired: %+v", deps)
	}
}
