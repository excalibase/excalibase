package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func requests(cpuMilli, memBytes int64) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
	}
}

// A pod marked for deletion still holds its request on the node until it has
// actually exited, so it must still be counted as requested.
func TestGetClusterCapacity_ATerminatingPodStillHoldsItsRequest(t *testing.T) {
	terminating := podWithRequests("leaving", "t1", corev1.PodRunning, 700, 1<<30)
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	terminating.Finalizers = []string{"example.com/hold"}
	c := newFakeClient(node("n1", 1000, 2<<30, false), terminating)

	cap, err := c.GetClusterCapacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cap.RequestedCPUMilli != 700 || cap.RequestedMemBytes != 1<<30 {
		t.Fatalf("requested = %dm %d, want the terminating pod counted", cap.RequestedCPUMilli, cap.RequestedMemBytes)
	}
}

// The scheduler charges a pod its overhead and the larger of its init and
// regular containers; counting less over-admits.
func TestGetClusterCapacity_CountsWhatTheSchedulerCharges(t *testing.T) {
	pod := podWithRequests("sandboxed", "t1", corev1.PodRunning, 100, 100<<20)
	pod.Spec.InitContainers = []corev1.Container{{Name: "init", Resources: corev1.ResourceRequirements{Requests: requests(400, 50<<20)}}}
	pod.Spec.Overhead = requests(50, 30<<20)
	c := newFakeClient(node("n1", 4000, 8<<30, false), pod)

	cap, err := c.GetClusterCapacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cap.RequestedCPUMilli != 450 || cap.RequestedMemBytes != 130<<20 {
		t.Fatalf("requested = %dm %d, want 450m and 130Mi", cap.RequestedCPUMilli, cap.RequestedMemBytes)
	}
}

func TestLiveAppPods_LeavesOutTerminatingAndFinishedPods(t *testing.T) {
	labels := map[string]string{"excalibase.io/app": "app-1", "app.kubernetes.io/managed-by": appManagedByValue}
	live := podWithRequests("live", testNamespace, corev1.PodRunning, 1, 1)
	live.Labels = labels
	leaving := podWithRequests("leaving", testNamespace, corev1.PodRunning, 1, 1)
	leaving.Labels = labels
	now := metav1.Now()
	leaving.DeletionTimestamp = &now
	leaving.Finalizers = []string{"example.com/hold"}
	done := podWithRequests("done", testNamespace, corev1.PodFailed, 1, 1)
	done.Labels = labels
	other := podWithRequests("other", testNamespace, corev1.PodRunning, 1, 1)
	other.Labels = map[string]string{"excalibase.io/app": "app-2", "app.kubernetes.io/managed-by": appManagedByValue}
	c := newFakeClient(live, leaving, done, other)

	pods, err := c.LiveAppPods(context.Background(), testNamespace, "app-1")
	if err != nil || pods.Count != 1 || pods.CPUMilli != 1 || pods.MaxCPUMilli != 1 {
		t.Fatalf("LiveAppPods = %+v, %v; want the one live pod", pods, err)
	}
}

func TestRuntimeClassPlacement(t *testing.T) {
	withOverhead := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc",
		Overhead:   &nodev1.Overhead{PodFixed: requests(100, 64<<20)},
		Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"excalibase.io/gvisor": "true"}}}
	plain := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "plain"}, Handler: "runc"}
	c := newFakeClient(withOverhead, plain)

	placement, err := c.RuntimeClassPlacement(context.Background(), "gvisor")
	if err != nil || placement.OverheadCPUMilli != 100 || placement.OverheadMemBytes != 64<<20 || placement.NodeSelector["excalibase.io/gvisor"] != "true" {
		t.Fatalf("placement = %+v %v", placement, err)
	}
	if placement, err := c.RuntimeClassPlacement(context.Background(), "plain"); err != nil || placement.OverheadCPUMilli != 0 || placement.NodeSelector != nil {
		t.Fatalf("plain = %+v %v", placement, err)
	}
	if _, err := c.RuntimeClassPlacement(context.Background(), "missing"); err == nil {
		t.Fatal("a missing runtime class must be an error, not zero overhead")
	}
}

func TestGetClusterCapacity_PerNode(t *testing.T) {
	big := node("big", 4000, 8<<30, false)
	big.Labels = map[string]string{"excalibase.io/gvisor": "true"}
	small := node("small", 1000, 1<<30, false)
	onBig := podWithRequests("on-big", "t1", corev1.PodRunning, 3500, 1<<30)
	onBig.Spec.NodeName = "big"
	c := newFakeClient(big, small, onBig, node("cordoned", 9000, 9<<30, true))

	cap, err := c.GetClusterCapacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cap.Nodes) != 2 {
		t.Fatalf("nodes = %+v", cap.Nodes)
	}
	var bigNode NodeCapacity
	for _, n := range cap.Nodes {
		if n.Name == "big" {
			bigNode = n
		}
	}
	if bigNode.RequestedCPUMilli != 3500 || bigNode.Fits(600, 1<<20, 0) || !bigNode.Fits(500, 1<<20, 0) {
		t.Fatalf("big = %+v", bigNode)
	}
	if !bigNode.Matches(map[string]string{"excalibase.io/gvisor": "true"}) || bigNode.Matches(map[string]string{"x": "y"}) {
		t.Fatal("label matching is wrong")
	}
	if bigNode.Fits(400, 1<<20, 20) {
		t.Fatal("the headroom must be held back per node too")
	}
}
