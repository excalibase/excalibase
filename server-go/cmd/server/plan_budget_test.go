package main

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

func budgetConfig(mode string, flags ...features.Feature) config.AppConfig {
	return config.AppConfig{DeploymentMode: mode, DarkFeatures: flags, PlanBudget: config.PlanBudgetConfig{
		Free: config.PlanNumbers{MaxProjects: 1}, Standard: config.PlanNumbers{MaxProjects: 20},
	}}
}

func TestThePlanBudgetChecksOnlyOnACloudInstallWithTheFlagOn(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		config config.AppConfig
		active bool
	}{
		{"flag off", budgetConfig("cloud"), false},
		{"flag on, cloud", budgetConfig("cloud", features.PlanBudget), true},
		{"flag on, self-hosted", budgetConfig("selfhosted", features.PlanBudget), false},
	}
	for _, c := range cases {
		if got := planBudgetGate(c.config).Active(ctx); got != c.active {
			t.Errorf("%s: active %v, want %v", c.name, got, c.active)
		}
	}
}

func TestFreeHoldsOneProjectOnlyWhileTheBudgetIsOn(t *testing.T) {
	ctx := context.Background()
	if _, ok := planProjectCap(budgetConfig("cloud"))(ctx, domain.Free); ok {
		t.Fatal("flag off must leave the tier row's number")
	}
	on := planProjectCap(budgetConfig("cloud", features.PlanBudget))
	if limit, ok := on(ctx, domain.Free); !ok || limit != 1 {
		t.Fatalf("free: %d %v", limit, ok)
	}
	if limit, ok := on(ctx, domain.Standard); !ok || limit != 20 {
		t.Fatalf("standard: %d %v", limit, ok)
	}
	if _, ok := on(ctx, domain.Enterprise); ok {
		t.Fatal("enterprise has no budget number")
	}
}

func TestASelfHostedInstallKeepsNoProjectCapEvenWithTheFlagOn(t *testing.T) {
	projectCap := planProjectCap(budgetConfig("selfhosted", features.PlanBudget))
	if _, ok := projectCap(context.Background(), domain.Free); ok {
		t.Fatal("self-hosters are unlimited: the budget's project cap must not apply")
	}
}
