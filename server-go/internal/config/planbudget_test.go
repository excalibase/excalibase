package config

import "testing"

func clearPlanBudgetEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for _, plan := range []string{"FREE", "STANDARD"} {
		for _, field := range []string{"CPU_MILLI", "MEMORY_MIB", "DISK_GIB", "MAX_PROJECTS", "MAX_SERVICES"} {
			t.Setenv("PLANBUDGET_"+plan+"_"+field, "")
		}
	}
}

func TestPlanBudgetDefaultsAreTheDecidedNumbers(t *testing.T) {
	clearPlanBudgetEnv(t)
	got := Load().PlanBudget
	if got.Free != (PlanNumbers{2000, 4096, 20, 1, 6}) {
		t.Errorf("free %+v: 2 vCPU, 4 GiB, 20 GiB, one project (the owner's), six services", got.Free)
	}
	if got.Standard != (PlanNumbers{4000, 8192, 30, 20, 50}) {
		t.Errorf("standard %+v", got.Standard)
	}
	if err := Load().Validate(); err != nil {
		t.Errorf("defaults do not validate: %v", err)
	}
}

func TestPlanBudgetNumbersCanBeChangedAndABadOneStopsTheBoot(t *testing.T) {
	clearPlanBudgetEnv(t)
	t.Setenv("PLANBUDGET_FREE_CPU_MILLI", "3000")
	if got := Load().PlanBudget.Free.CPUMilli; got != 3000 {
		t.Fatalf("%d", got)
	}
	for _, bad := range []string{"abc", "0", "-1", "1.5"} {
		t.Setenv("PLANBUDGET_FREE_MAX_PROJECTS", bad)
		if Load().Validate() == nil {
			t.Errorf("PLANBUDGET_FREE_MAX_PROJECTS=%q was accepted", bad)
		}
	}
}
