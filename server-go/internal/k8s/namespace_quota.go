package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const namespaceQuotaName = "namespace-quota"

// NamespaceQuota is the object-count ceiling of one project namespace (EXC-329).
type NamespaceQuota struct {
	Pods     int
	PVCs     int
	Services int
}

// DefaultNamespaceQuota is what a namespace starts with and the floor a plan
// never goes below: the database, watcher, function runtime and CNPG jobs.
var DefaultNamespaceQuota = NamespaceQuota{Pods: 20, PVCs: 7, Services: 15}

// PlanQuotaInputs are what the organisation's plan lets the project run.
type PlanQuotaInputs struct {
	DBInstances int
	// Apps is the plan's app count, or the apps the project holds when more (after a downgrade).
	Apps           int
	AppMaxReplicas int
}

// NamespaceQuotaForPlan sizes the quota so it never refuses what the plan allows
// (EXC-524): each app may run its replicas plus one rollout surge and one disk
// job, keep one disk plus the volume it is copied onto while lowered, and has
// one Service. Storage bytes stay governed by the plan caps and storage budget.
func NamespaceQuotaForPlan(plan PlanQuotaInputs) NamespaceQuota {
	base := DefaultNamespaceQuota
	return NamespaceQuota{
		Pods:     base.Pods + plan.Apps*(plan.AppMaxReplicas+2),
		PVCs:     max(base.PVCs, plan.DBInstances+2*plan.Apps),
		Services: base.Services + plan.Apps,
	}
}

func (q NamespaceQuota) hard() corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourcePods:                   *resource.NewQuantity(int64(q.Pods), resource.DecimalSI),
		corev1.ResourcePersistentVolumeClaims: *resource.NewQuantity(int64(q.PVCs), resource.DecimalSI),
		corev1.ResourceServices:               *resource.NewQuantity(int64(q.Services), resource.DecimalSI),
	}
}

func buildNamespaceQuota(namespace string, q NamespaceQuota) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namespaceQuotaName,
			Namespace: namespace,
			Labels:    map[string]string{componentLabelKey: "quota"},
		},
		Spec: corev1.ResourceQuotaSpec{Hard: q.hard()},
	}
}

// EnsureNamespaceQuota converges the namespace's quota to q, creating it if absent.
func (c *Client) EnsureNamespaceQuota(ctx context.Context, namespace string, q NamespaceQuota) error {
	quotas := c.clientset.CoreV1().ResourceQuotas(namespace)
	existing, err := quotas.Get(ctx, namespaceQuotaName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := quotas.Create(ctx, buildNamespaceQuota(namespace, q), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create namespace quota: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read namespace quota: %w", err)
	}
	if sameHard(existing.Spec.Hard, q.hard()) {
		return nil
	}
	updated := existing.DeepCopy()
	updated.Spec.Hard = q.hard()
	if _, err := quotas.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update namespace quota: %w", err)
	}
	return nil
}

func sameHard(a, b corev1.ResourceList) bool {
	if len(a) != len(b) {
		return false
	}
	for name, want := range b {
		got, ok := a[name]
		if !ok || got.Cmp(want) != 0 {
			return false
		}
	}
	return true
}
