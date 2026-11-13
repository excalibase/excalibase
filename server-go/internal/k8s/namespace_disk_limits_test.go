package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestAppResourceRequirements_CapsEphemeralStoragePerTier(t *testing.T) {
	var previous int64
	for _, tier := range []domain.TierType{domain.Free, domain.Standard, domain.Enterprise} {
		cfg, _ := config.GetAppTierConfig(tier)
		resources, err := appResourceRequirements(cfg)
		if err != nil {
			t.Fatal(err)
		}
		limit, request := resources.Limits[corev1.ResourceEphemeralStorage], resources.Requests[corev1.ResourceEphemeralStorage]
		if limit.IsZero() || request.IsZero() {
			t.Fatalf("%s: an app pod needs an ephemeral-storage request and limit, got %v / %v", tier, request, limit)
		}
		if limit.Value() <= previous {
			t.Errorf("%s: limit %s does not grow with the tier", tier, limit.String())
		}
		previous = limit.Value()
	}
}

func TestCreateProjectNamespace_AddsADiskLimitRangeBackstop(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.CreateProjectNamespace(ctx, "org1-proj-disk", "org1"); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	lr, err := c.clientset.CoreV1().LimitRanges("org1-proj-disk").Get(ctx, namespaceLimitRangeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("a project namespace needs a LimitRange so a pod with no disk limit gets one: %v", err)
	}
	if len(lr.Spec.Limits) != 1 || lr.Spec.Limits[0].Type != corev1.LimitTypeContainer {
		t.Fatalf("limits = %+v", lr.Spec.Limits)
	}
	item := lr.Spec.Limits[0]
	if item.Default.StorageEphemeral().IsZero() || item.DefaultRequest.StorageEphemeral().IsZero() {
		t.Fatalf("default limit and request must both name ephemeral storage: %+v", item)
	}
	// The backstop follows the largest app tier, so it never cuts what a tier allows.
	if largest := config.LargestAppEphemeralStorageLimit(); item.Default.StorageEphemeral().Cmp(largest) != 0 {
		t.Errorf("default limit %s, want the largest tier's %s", item.Default.StorageEphemeral(), largest.String())
	}
	if item.DefaultRequest.StorageEphemeral().Cmp(*item.Default.StorageEphemeral()) >= 0 {
		t.Errorf("the default request must stay below the default limit, or every pod reserves its whole ceiling")
	}
}

func TestEnsureNamespaceQuota_AlsoRestoresTheLimitRange(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.EnsureNamespaceQuota(ctx, "org1-proj-restore", DefaultNamespaceQuota); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := c.clientset.CoreV1().LimitRanges("org1-proj-restore").Get(ctx, namespaceLimitRangeName, metav1.GetOptions{}); err != nil {
		t.Fatalf("the plan sync must leave the LimitRange in place: %v", err)
	}
}
