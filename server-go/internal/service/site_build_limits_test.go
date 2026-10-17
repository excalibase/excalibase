package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// A project's build limits follow its organisation's plan now, admin edits included (EXC-354).
func TestSiteBuildLimits_ReadTheOrganisationsPlan(t *testing.T) {
	plans := fixedTierConfigs{
		domain.Free:       {SiteBuilds: config.SiteBuildTierLimits{CPUMillis: 2000, Memory: "2Gi", Disk: "5Gi", TimeoutSeconds: 600, Concurrency: 1}},
		domain.Standard:   {SiteBuilds: config.SiteBuildTierLimits{CPUMillis: 3000, Memory: "6Gi", Disk: "8Gi", TimeoutSeconds: 1200, Concurrency: 2}},
		domain.Enterprise: {SiteBuilds: config.SiteBuildTierLimits{CPUMillis: 2000, Memory: "4Gi", Disk: "20Gi", TimeoutSeconds: 1200, Concurrency: 2}},
	}
	limits := NewSiteBuildLimits(fixedPlan{tier: domain.Standard}, plans)
	got, err := limits.BuildLimits(context.Background(), "p1")
	want := sitebuilds.Limits{CPUMillis: 3000, Memory: "6Gi", Disk: "8Gi", TimeoutSeconds: 1200, ProjectConcurrency: 2}
	if err != nil || got != want {
		t.Fatalf("BuildLimits = %+v, %v; want %+v", got, err, want)
	}
	largest, err := limits.LargestBuild(context.Background())
	if err != nil || largest != (sitebuilds.BuildSize{CPUMillis: 3000, Memory: "6Gi", Disk: "20Gi"}) {
		t.Fatalf("LargestBuild = %+v, %v", largest, err)
	}
	for name, source := range map[string]sitebuilds.LimitsSource{
		"no plan":        NewSiteBuildLimits(fixedPlan{err: ErrOrgTierUnresolved}, plans),
		"no tier row":    NewSiteBuildLimits(fixedPlan{tier: domain.Enterprise}, fixedTierConfigs{}),
		"no plan source": NewSiteBuildLimits(nil, plans),
	} {
		if _, err := source.BuildLimits(context.Background(), "p1"); !errors.Is(err, ErrOrgTierUnresolved) {
			t.Errorf("%s: err = %v, want ErrOrgTierUnresolved", name, err)
		}
	}
	if _, err := NewSiteBuildLimits(fixedPlan{tier: domain.Free}, fixedTierConfigs{domain.Free: {}}).LargestBuild(context.Background()); !errors.Is(err, ErrOrgTierUnresolved) {
		t.Errorf("a plan with no row must not be skipped when sizing the quota: %v", err)
	}
}
