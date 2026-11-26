package k8s

import (
	"context"
	"errors"
	"testing"

	"helm.sh/helm/v3/pkg/release"
)

func TestMockClient_InstallRefusesAReleaseThatExists(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()
	if err := m.InstallHelmChart(ctx, "ns", "rel", "/chart", nil); err != nil {
		t.Fatal(err)
	}
	if err := m.InstallHelmChart(ctx, "ns", "rel", "/chart", nil); !errors.Is(err, ErrReleaseInUse) {
		t.Fatalf("a second install of one release: err = %v, want ErrReleaseInUse", err)
	}
}

func TestMockClient_ApplyInstallsThenUpgrades(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()
	for _, tag := range []string{"one", "two"} {
		if err := m.ApplyHelmChart(ctx, "ns", "rel", "/chart", map[string]interface{}{"tag": tag}); err != nil {
			t.Fatalf("apply %s: %v", tag, err)
		}
	}
	if got := m.HelmReleases["ns/rel"]["tag"]; got != "two" {
		t.Errorf("values = %v, want the upgrade's", got)
	}
}

func TestHelmApplyStepFor(t *testing.T) {
	cases := map[string]struct {
		status release.Status
		found  bool
		want   helmStep
	}{
		"no release":                {found: false, want: helmInstall},
		"deployed":                  {status: release.StatusDeployed, found: true, want: helmUpgrade},
		"failed":                    {status: release.StatusFailed, found: true, want: helmUpgrade},
		"superseded":                {status: release.StatusSuperseded, found: true, want: helmUpgrade},
		"stuck in pending upgrade":  {status: release.StatusPendingUpgrade, found: true, want: helmReinstall},
		"stuck in pending install":  {status: release.StatusPendingInstall, found: true, want: helmReinstall},
		"stuck in pending rollback": {status: release.StatusPendingRollback, found: true, want: helmReinstall},
		"uninstalling":              {status: release.StatusUninstalling, found: true, want: helmReinstall},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := helmApplyStepFor(c.status, c.found); got != c.want {
				t.Errorf("step = %v, want %v", got, c.want)
			}
		})
	}
}
