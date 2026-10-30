package config

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Each plan bounds one app build (EXC-547): the starting numbers come from the
// gVisor app-build spike, and builds never draw from a project's running budget.
func TestTiersCarryTheirAppBuildLimits(t *testing.T) {
	want := map[domain.TierType]AppBuildTierLimits{
		domain.Free:       {CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi", TimeoutSeconds: 15 * 60, Concurrency: 1, MaxImage: "2Gi"},
		domain.Standard:   {CPUMillis: 4000, Memory: "8Gi", Disk: "20Gi", TimeoutSeconds: 30 * 60, Concurrency: 2, MaxImage: "5Gi"},
		domain.Enterprise: {CPUMillis: 4000, Memory: "8Gi", Disk: "20Gi", TimeoutSeconds: 30 * 60, Concurrency: 2, MaxImage: "5Gi"},
	}
	for tier, limits := range want {
		got, err := GetTierConfig(tier)
		if err != nil {
			t.Fatalf("%s: %v", tier, err)
		}
		if got.AppBuilds != limits {
			t.Errorf("%s: app build limits %+v, want %+v", tier, got.AppBuilds, limits)
		}
		if got.AppBuilds.CPUMillis < MinSiteBuildCPUMillis {
			t.Errorf("%s: an app build gets less CPU than gVisor needs", tier)
		}
	}
}
