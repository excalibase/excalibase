package k8s

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

// The quota follows the plan (EXC-524): every database instance and every
// app's disk, plus the volume a disk is copied onto while it is lowered, so it
// never refuses what the plan allows. It never drops below today's floor.
func TestNamespaceQuotaForPlan(t *testing.T) {
	cases := map[string]struct {
		plan PlanQuotaInputs
		want NamespaceQuota
	}{
		"free keeps the floor": {PlanQuotaInputs{DBInstances: 1, Apps: 2, AppMaxReplicas: 1}, NamespaceQuota{Pods: 26, PVCs: 7, Services: 17}},
		"standard":             {PlanQuotaInputs{DBInstances: 3, Apps: 5, AppMaxReplicas: 3}, NamespaceQuota{Pods: 45, PVCs: 13, Services: 20}},
		"enterprise":           {PlanQuotaInputs{DBInstances: 5, Apps: 20, AppMaxReplicas: 3}, NamespaceQuota{Pods: 120, PVCs: 45, Services: 35}},
		"no apps is the floor": {PlanQuotaInputs{DBInstances: 5}, DefaultNamespaceQuota},
	}
	for name, tc := range cases {
		if got := NamespaceQuotaForPlan(tc.plan); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}

func quotaHard(t *testing.T, c *Client, ns string) corev1.ResourceList {
	t.Helper()
	q, err := c.clientset.CoreV1().ResourceQuotas(ns).Get(context.Background(), namespaceQuotaName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read quota: %v", err)
	}
	return q.Spec.Hard
}

func TestEnsureNamespaceQuota_RaisesAnExistingQuota(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.CreateProjectNamespace(ctx, "org1-proj-q", "org1"); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	want := NamespaceQuota{Pods: 120, PVCs: 45, Services: 35}
	if err := c.EnsureNamespaceQuota(ctx, "org1-proj-q", want); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	hard := quotaHard(t, c, "org1-proj-q")
	pods, pvcs, services := hard[corev1.ResourcePods], hard[corev1.ResourcePersistentVolumeClaims], hard[corev1.ResourceServices]
	if pods.Value() != 120 || pvcs.Value() != 45 || services.Value() != 35 {
		t.Fatalf("quota = %v", hard)
	}
	// Unchanged values are not rewritten.
	if err := c.EnsureNamespaceQuota(ctx, "org1-proj-q", want); err != nil {
		t.Fatalf("ensure again: %v", err)
	}
}

func TestEnsureNamespaceQuota_CreatesAMissingOne(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsureNamespaceQuota(context.Background(), "org1-proj-q", DefaultNamespaceQuota); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	hard := quotaHard(t, c, "org1-proj-q")
	if pvcs := hard[corev1.ResourcePersistentVolumeClaims]; pvcs.Value() != 7 {
		t.Fatalf("quota = %v", hard)
	}
}

func TestEnsureNamespaceQuota_ErrorsPropagate(t *testing.T) {
	for _, verb := range []string{"get", "update"} {
		c := newFakeClient()
		ctx := context.Background()
		if err := c.CreateProjectNamespace(ctx, "org1-proj-q", "org1"); err != nil {
			t.Fatal(err)
		}
		c.clientset.(interface {
			PrependReactor(string, string, ktesting.ReactionFunc)
		}).PrependReactor(verb, "resourcequotas", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(verb + " refused")
		})
		if err := c.EnsureNamespaceQuota(ctx, "org1-proj-q", NamespaceQuota{Pods: 99, PVCs: 99, Services: 99}); err == nil {
			t.Errorf("a refused %s must fail", verb)
		}
	}
}

// A quota is never set below what the namespace already holds, so a downgrade
// refuses new objects and keeps the running ones (EXC-524).
func TestEnsureNamespaceQuota_NeverBelowCurrentUsage(t *testing.T) {
	const ns = "org1-proj-used"
	objects := []runtime.Object{}
	for i := range 10 {
		objects = append(objects,
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("claim-%d", i), Namespace: ns}},
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("svc-%d", i), Namespace: ns}},
		)
	}
	for i := range 30 {
		phase := corev1.PodRunning
		if i%10 == 0 {
			phase = corev1.PodSucceeded // finished pods are not counted by the quota
		}
		objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod-%d", i), Namespace: ns}, Status: corev1.PodStatus{Phase: phase}})
	}
	c := newFakeClient(objects...)
	if err := c.EnsureNamespaceQuota(context.Background(), ns, DefaultNamespaceQuota); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	hard := quotaHard(t, c, ns)
	pods, pvcs, services := hard[corev1.ResourcePods], hard[corev1.ResourcePersistentVolumeClaims], hard[corev1.ResourceServices]
	if pods.Value() != 27 || pvcs.Value() != 10 || services.Value() != 15 {
		t.Fatalf("quota = pods %d pvcs %d services %d, want 27/10/15 (usage above the plan kept)", pods.Value(), pvcs.Value(), services.Value())
	}
}

func TestEnsureNamespaceQuota_AFailedUsageReadChangesNothing(t *testing.T) {
	for _, resource := range []string{"pods", "persistentvolumeclaims", "services"} {
		c := newFakeClient()
		c.clientset.(interface {
			PrependReactor(string, string, ktesting.ReactionFunc)
		}).PrependReactor("list", resource, func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("list refused")
		})
		if err := c.EnsureNamespaceQuota(context.Background(), "org1-proj-q", DefaultNamespaceQuota); err == nil {
			t.Errorf("%s: a quota sized without knowing the usage must be refused", resource)
		}
	}
}
