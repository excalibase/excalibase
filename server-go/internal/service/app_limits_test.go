package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// The limit follows the organisation's plan now, admin edits included (EXC-524).
func TestAppLimits_ReadTheOrganisationsPlan(t *testing.T) {
	limits := NewAppLimits(fixedPlan{tier: domain.Free},
		fixedTierConfigs{domain.Free: {MaxApps: 2}, domain.Standard: {MaxApps: 5}})
	if got, err := limits.MaxApps(context.Background(), "p1"); err != nil || got != 2 {
		t.Fatalf("MaxApps = %d, %v; want 2", got, err)
	}
	for name, limits := range map[string]apphost.AppLimits{
		"no plan":            NewAppLimits(fixedPlan{err: ErrOrgTierUnresolved}, fixedTierConfigs{}),
		"no tier row":        NewAppLimits(fixedPlan{tier: domain.Enterprise}, fixedTierConfigs{}),
		"no tier config set": NewAppLimits(fixedPlan{tier: domain.Free}, nil),
		"no plan source":     NewAppLimits(nil, fixedTierConfigs{}),
	} {
		if _, err := limits.MaxApps(context.Background(), "p1"); !errors.Is(err, ErrOrgTierUnresolved) {
			t.Errorf("%s: err = %v, want ErrOrgTierUnresolved", name, err)
		}
	}
}
