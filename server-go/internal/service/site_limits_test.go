package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// A project's Sites limits follow its organisation's plan now, admin edits included (EXC-620).
func TestSiteLimits_ReadTheOrganisationsPlan(t *testing.T) {
	standard := config.SiteTierLimits{MaxSites: 20, MaxDeployBytes: 100 << 20, MaxFiles: 20_000, MaxFileBytes: 25 << 20, MaxDeploysPerHour: 120, MaxCustomDomains: 5}
	limits := NewSiteLimits(fixedPlan{tier: domain.Standard},
		fixedTierConfigs{domain.Free: {Sites: config.SiteTierLimits{MaxSites: 5}}, domain.Standard: {Sites: standard}})
	got, err := limits.DeployLimits(context.Background(), "p1")
	want := sites.Limits{MaxSites: 20, MaxTotalBytes: 100 << 20, MaxFiles: 20_000, MaxFileBytes: 25 << 20, MaxDeploysPerHour: 120, MaxCustomDomains: 5}
	if err != nil || got != want {
		t.Fatalf("DeployLimits = %+v, %v; want %+v", got, err, want)
	}
	for name, limits := range map[string]sites.DeployLimiter{
		"no plan":            NewSiteLimits(fixedPlan{err: ErrOrgTierUnresolved}, fixedTierConfigs{}),
		"no tier row":        NewSiteLimits(fixedPlan{tier: domain.Enterprise}, fixedTierConfigs{}),
		"no tier config set": NewSiteLimits(fixedPlan{tier: domain.Free}, nil),
		"no plan source":     NewSiteLimits(nil, fixedTierConfigs{}),
	} {
		if _, err := limits.DeployLimits(context.Background(), "p1"); !errors.Is(err, ErrOrgTierUnresolved) {
			t.Errorf("%s: err = %v, want ErrOrgTierUnresolved", name, err)
		}
	}
}
