//go:build live

package k8s

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sTerminatingPodHoldsItsRequest -v -timeout 30m
//
// A pod that ignores SIGTERM stays Terminating for its whole grace period and
// keeps its CPU; the capacity reading must keep counting it, exactly as the
// scheduler does, or an admission check would promise room that is not there.
func TestK3sTerminatingPodHoldsItsRequest(t *testing.T) {
	g := startGVisorK3s(t, gvisorPlatformSystrap)
	ctx := context.Background()
	namespace := g.createNamespace(t, "capacity")

	before, err := g.client.GetClusterCapacity(ctx)
	if err != nil {
		t.Fatalf("capacity: %v", err)
	}
	hogCPU := before.FreeCPUMilli() - 200
	if hogCPU < 500 {
		t.Skipf("the node has only %dm free", before.FreeCPUMilli())
	}
	g.startPod(t, namespace, "hog", hogCPU, 600)
	if err := g.clientset.CoreV1().Pods(namespace).Delete(ctx, "hog", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete hog: %v", err)
	}
	time.Sleep(5 * time.Second)
	hog, err := g.clientset.CoreV1().Pods(namespace).Get(ctx, "hog", metav1.GetOptions{})
	if err != nil || hog.DeletionTimestamp == nil {
		t.Fatalf("the hog must still be terminating: %v", err)
	}

	after, err := g.client.GetClusterCapacity(ctx)
	if err != nil {
		t.Fatalf("capacity: %v", err)
	}
	if after.RequestedCPUMilli-before.RequestedCPUMilli < hogCPU {
		t.Fatalf("the terminating pod's %dm is not counted: requested %dm before, %dm after",
			hogCPU, before.RequestedCPUMilli, after.RequestedCPUMilli)
	}

	// The scheduler agrees: a pod asking for the hog's CPU cannot be placed while it terminates.
	g.createPod(t, namespace, "newcomer", hogCPU, 0)
	eventually(t, "the newcomer is unschedulable", time.Minute, func() bool {
		pod, err := g.clientset.CoreV1().Pods(namespace).Get(ctx, "newcomer", metav1.GetOptions{})
		return err == nil && podUnschedulable(pod)
	})
	t.Logf("free after delete: %dm; newcomer needs %dm and is pending", after.FreeCPUMilli(), hogCPU)
}

func podUnschedulable(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Reason == corev1.PodReasonUnschedulable {
			return true
		}
	}
	return false
}

func (g *gvisorCluster) createPod(t *testing.T, namespace, name string, cpuMilli, graceSeconds int64) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: &graceSeconds,
			Containers: []corev1.Container{{
				Name: "hold", Image: "busybox:1.36",
				Command:   []string{"sh", "-c", `trap "" TERM; while true; do sleep 1; done`},
				Resources: corev1.ResourceRequirements{Requests: requests(cpuMilli, 16<<20)},
			}},
		},
	}
	if _, err := g.clientset.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

func (g *gvisorCluster) startPod(t *testing.T, namespace, name string, cpuMilli, graceSeconds int64) {
	t.Helper()
	g.createPod(t, namespace, name, cpuMilli, graceSeconds)
	eventually(t, name+" runs", 3*time.Minute, func() bool {
		pod, err := g.clientset.CoreV1().Pods(namespace).Get(context.Background(), name, metav1.GetOptions{})
		return err == nil && pod.Status.Phase == corev1.PodRunning
	})
}
