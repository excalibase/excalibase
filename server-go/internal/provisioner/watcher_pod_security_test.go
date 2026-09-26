package provisioner

import (
	"context"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	appsv1 "k8s.io/api/apps/v1"
	psaapi "k8s.io/pod-security-admission/api"
	"k8s.io/pod-security-admission/policy"
	"sigs.k8s.io/yaml"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const watcherChartDir = "../../charts/excalibase-watcher-go"

// renderWatcherDeployment renders the chart with exactly the values DeployWatcher installs it with.
func renderWatcherDeployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, watcherChartDir)
	prov.SetWatcherImage(pinnedWatcher)
	if err := prov.DeployWatcher(context.Background(), watcherSpecForTest()); err != nil {
		t.Fatalf("DeployWatcher: %v", err)
	}
	chart, err := loader.Load(watcherChartDir)
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}
	options := chartutil.ReleaseOptions{Name: watcherReleaseName, Namespace: "org1-proj", IsInstall: true}
	values, err := chartutil.ToRenderValues(chart, mock.HelmReleases["org1-proj/"+watcherReleaseName], options, chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatalf("render values: %v", err)
	}
	manifests, err := engine.Render(chart, values)
	if err != nil {
		t.Fatalf("render chart: %v", err)
	}
	for name, manifest := range manifests {
		if !strings.HasSuffix(name, "deployment.yaml") {
			continue
		}
		var deployment appsv1.Deployment
		if err := yaml.Unmarshal([]byte(manifest), &deployment); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return &deployment
	}
	t.Fatal("the chart rendered no Deployment")
	return nil
}

// Tenant namespaces enforce baseline and warn on restricted; the watcher meets restricted so it raises no warning.
func TestWatcherPodMeetsTheRestrictedLevel(t *testing.T) {
	deployment := renderWatcherDeployment(t)
	evaluator, err := policy.NewEvaluator(policy.DefaultChecks(), nil)
	if err != nil {
		t.Fatalf("pod security evaluator: %v", err)
	}
	template := deployment.Spec.Template
	level := psaapi.LevelVersion{Level: psaapi.LevelRestricted, Version: psaapi.LatestVersion()}
	for _, result := range evaluator.EvaluatePod(level, &template.ObjectMeta, &template.Spec) {
		if !result.Allowed {
			t.Errorf("watcher violates restricted: %s (%s)", result.ForbiddenReason, result.ForbiddenDetail)
		}
	}
}
