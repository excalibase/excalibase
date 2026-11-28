package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// PlanNumbers is what one plan holds in the CPU/RAM budget model (EXC-901).
type PlanNumbers struct {
	CPUMilli    int
	MemoryMiB   int
	DiskGiB     int
	MaxProjects int
	// MaxServices counts databases, apps and a functions runtime together.
	MaxServices int
	// The allowance is the part of the budget that is never charged: the whole
	// ceiling a plan keeps when its credit is gone. Zero charges everything.
	AllowanceCPUMilli  int
	AllowanceMemoryMiB int
	AllowanceDiskGiB   int
}

// PlanBudgetConfig is the plan table behind FEATURE_PLANBUDGET. None of it is
// decided in code, and a mistyped value stops the boot instead of falling back.
// ENTERPRISE has no budget.
type PlanBudgetConfig struct {
	Free     PlanNumbers
	Standard PlanNumbers

	err error
}

func loadPlanBudget() PlanBudgetConfig {
	var errs []string
	number := func(key string, fallback int) int {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			return fallback
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Sprintf("%s=%q is not a whole number of at least 1", key, raw))
			return fallback
		}
		return n
	}
	allowance := func(key string, fallback int) int {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			return fallback
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Sprintf("%s=%q is not a whole number of at least 0", key, raw))
			return fallback
		}
		return n
	}
	plan := func(prefix string, d PlanNumbers) PlanNumbers {
		return PlanNumbers{
			AllowanceCPUMilli:  allowance(prefix+"_ALLOWANCE_CPU_MILLI", d.AllowanceCPUMilli),
			AllowanceMemoryMiB: allowance(prefix+"_ALLOWANCE_MEMORY_MIB", d.AllowanceMemoryMiB),
			AllowanceDiskGiB:   allowance(prefix+"_ALLOWANCE_DISK_GIB", d.AllowanceDiskGiB),
			CPUMilli:           number(prefix+"_CPU_MILLI", d.CPUMilli),
			MemoryMiB:          number(prefix+"_MEMORY_MIB", d.MemoryMiB),
			DiskGiB:            number(prefix+"_DISK_GIB", d.DiskGiB),
			MaxProjects:        number(prefix+"_MAX_PROJECTS", d.MaxProjects),
			MaxServices:        number(prefix+"_MAX_SERVICES", d.MaxServices),
		}
	}
	cfg := PlanBudgetConfig{
		Free:     plan("PLANBUDGET_FREE", PlanNumbers{CPUMilli: 2000, MemoryMiB: 4096, DiskGiB: 20, MaxProjects: 1, MaxServices: 6, AllowanceCPUMilli: 1000, AllowanceMemoryMiB: 1024, AllowanceDiskGiB: 10}),
		Standard: plan("PLANBUDGET_STANDARD", PlanNumbers{CPUMilli: 4000, MemoryMiB: 8192, DiskGiB: 30, MaxProjects: 20, MaxServices: 50}),
	}
	if len(errs) > 0 {
		cfg.err = fmt.Errorf("plan budget settings: %s", strings.Join(errs, "; "))
	}
	return cfg
}

func (c AppConfig) validatePlanBudget() error { return c.PlanBudget.err }
