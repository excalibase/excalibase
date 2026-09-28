//go:build live

package k8s

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EXC-524: an ENTERPRISE project may hold 20 apps with a disk each; the API
// server's quota must admit what the plan allows once a deploy has sized it.
// Run with: go test ./internal/k8s/ -tags=live -run TestK3sNamespaceQuotaFollowsThePlan -v -count=1
func TestK3sNamespaceQuotaFollowsThePlan(t *testing.T) {
	lab := &egressLab{ctx: context.Background()}
	lab.startCluster(t)
	const ns = "org1-proj-quota"
	if err := lab.client.CreateProjectNamespace(lab.ctx, ns, "org1"); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	claim := func(i int) error {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("claim-%d", i), Namespace: ns},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
			},
		}
		_, err := lab.cs.CoreV1().PersistentVolumeClaims(ns).Create(lab.ctx, pvc, metav1.CreateOptions{})
		return err
	}
	for i := range DefaultNamespaceQuota.PVCs {
		if err := claim(i); err != nil {
			t.Fatalf("claim %d under the default quota: %v", i, err)
		}
	}
	if err := claim(DefaultNamespaceQuota.PVCs); err == nil {
		t.Fatal("control: the default quota must refuse an 8th claim")
	}

	enterprise := NamespaceQuotaForPlan(PlanQuotaInputs{DBInstances: 5, Apps: 20, AppMaxReplicas: 3})
	if err := lab.client.EnsureNamespaceQuota(lab.ctx, ns, enterprise); err != nil {
		t.Fatalf("size the quota for ENTERPRISE: %v", err)
	}
	// The quota controller recomputes usage after an update; admission follows.
	deadline := time.Now().Add(time.Minute)
	next := DefaultNamespaceQuota.PVCs
	for next < 5+20 {
		err := claim(next)
		if err == nil {
			next++
			continue
		}
		if time.Now().After(deadline) {
			t.Fatalf("claim %d refused under the ENTERPRISE quota: %v", next, err)
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("%d claims admitted: every database instance and one disk per app of the plan", next)
}
