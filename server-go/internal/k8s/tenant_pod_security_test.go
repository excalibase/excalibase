package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	psaapi "k8s.io/pod-security-admission/api"
	"k8s.io/pod-security-admission/policy"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestCreateProjectNamespace_EnforcesBaselineAndWarnsRestricted(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.CreateProjectNamespace(ctx, "org-1-proj", "org-1"); err != nil {
		t.Fatalf("CreateProjectNamespace: %v", err)
	}
	ns, err := c.clientset.CoreV1().Namespaces().Get(ctx, "org-1-proj", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace lookup: %v", err)
	}
	want := map[string]string{
		"pod-security.kubernetes.io/enforce":         "baseline",
		"pod-security.kubernetes.io/enforce-version": "latest",
		"pod-security.kubernetes.io/warn":            "restricted",
		"pod-security.kubernetes.io/warn-version":    "latest",
		"pod-security.kubernetes.io/audit":           "restricted",
		"pod-security.kubernetes.io/audit-version":   "latest",
	}
	for key, value := range want {
		if ns.Labels[key] != value {
			t.Errorf("label %s = %q, want %q", key, ns.Labels[key], value)
		}
	}
}

// assertPodMeets fails for every check of the given Pod Security level the pod violates.
func assertPodMeets(t *testing.T, level psaapi.Level, name string, meta *metav1.ObjectMeta, spec *corev1.PodSpec) {
	t.Helper()
	evaluator, err := policy.NewEvaluator(policy.DefaultChecks(), nil)
	if err != nil {
		t.Fatalf("pod security evaluator: %v", err)
	}
	results := evaluator.EvaluatePod(psaapi.LevelVersion{Level: level, Version: psaapi.LatestVersion()}, meta, spec)
	for _, result := range results {
		if !result.Allowed {
			t.Errorf("%s violates %s: %s (%s)", name, level, result.ForbiddenReason, result.ForbiddenDetail)
		}
	}
}

func TestTenantPodsMeetTheBaselineLevel(t *testing.T) {
	for name, app := range map[string]func() *apphost.App{"minimal app": minimalApp, "full app": fullApp} {
		workload := mustRender(t, app(), newResolver())
		template := workload.Deployment.Spec.Template
		assertPodMeets(t, psaapi.LevelBaseline, name, &template.ObjectMeta, &template.Spec)
	}
	for _, tier := range []domain.TierType{domain.Free, domain.Standard, domain.Enterprise} {
		deployment := buildDenoDeployment(testNamespace, DenoRuntimeSpec{Image: "excalibase/deno-runtime:1.0.0", RuntimeSecret: "s", Tier: string(tier)})
		template := deployment.Spec.Template
		assertPodMeets(t, psaapi.LevelBaseline, "deno runtime "+string(tier), &template.ObjectMeta, &template.Spec)
	}
}
