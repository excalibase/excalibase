package config

import "testing"

func clearPlanBudgetEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for _, plan := range []string{"FREE", "STANDARD"} {
		for _, field := range []string{"CPU_MILLI", "MEMORY_MIB", "DISK_GIB", "MAX_PROJECTS", "MAX_SERVICES", "ALLOWANCE_CPU_MILLI", "ALLOWANCE_MEMORY_MIB", "ALLOWANCE_DISK_GIB"} {
			t.Setenv("PLANBUDGET_"+plan+"_"+field, "")
		}
	}
}

func TestPlanBudgetDefaultsAreTheDecidedNumbers(t *testing.T) {
	clearPlanBudgetEnv(t)
	got := Load().PlanBudget
	if got.Free != (PlanNumbers{CPUMilli: 2000, MemoryMiB: 4096, DiskGiB: 20, MaxProjects: 1, MaxServices: 6, AllowanceCPUMilli: 1000, AllowanceMemoryMiB: 1024, AllowanceDiskGiB: 10}) {
		t.Errorf("free %+v: 2 vCPU, 4 GiB, 20 GiB, one project (the owner's), six services, a free allowance of 1 vCPU, 1 GiB, 10 GiB", got.Free)
	}
	if got.Standard != (PlanNumbers{CPUMilli: 4000, MemoryMiB: 8192, DiskGiB: 30, MaxProjects: 20, MaxServices: 50}) {
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

func TestAnAllowanceCanBeZeroOrChangedAndABadOneStopsTheBoot(t *testing.T) {
	clearPlanBudgetEnv(t)
	t.Setenv("PLANBUDGET_FREE_ALLOWANCE_CPU_MILLI", "0")
	t.Setenv("PLANBUDGET_STANDARD_ALLOWANCE_DISK_GIB", "5")
	got := Load().PlanBudget
	if got.Free.AllowanceCPUMilli != 0 || got.Standard.AllowanceDiskGiB != 5 {
		t.Fatalf("%+v %+v", got.Free, got.Standard)
	}
	if err := Load().Validate(); err != nil {
		t.Fatalf("a zero allowance is allowed: %v", err)
	}
	for _, bad := range []string{"abc", "-1", "1.5"} {
		t.Setenv("PLANBUDGET_FREE_ALLOWANCE_DISK_GIB", bad)
		if Load().Validate() == nil {
			t.Errorf("PLANBUDGET_FREE_ALLOWANCE_DISK_GIB=%q was accepted", bad)
		}
	}
}
