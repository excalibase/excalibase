package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func nodeWithPodLimit(name string, pods int64) *corev1.Node {
	n := node(name, 4000, 8<<30, false)
	n.Status.Allocatable[corev1.ResourcePods] = *resource.NewQuantity(pods, resource.DecimalSI)
	return n
}

func podOn(name, nodeName string, phase corev1.PodPhase) *corev1.Pod {
	p := podWithRequests(name, "ns", phase, 1, 1)
	p.Spec.NodeName = nodeName
	return p
}

// A node holds as many pods as its kubelet allows; a finished pod frees its slot and an unscheduled one never held one.
func TestGetClusterCapacity_CountsPodSlotsPerNode(t *testing.T) {
	c := newFakeClient(nodeWithPodLimit("n1", 5), nodeWithPodLimit("n2", 3),
		podOn("a", "n1", corev1.PodRunning), podOn("b", "n1", corev1.PodRunning),
		podOn("c", "n1", corev1.PodSucceeded), podOn("d", "n2", corev1.PodPending),
		podOn("unscheduled", "", corev1.PodPending))

	capacity, err := c.GetClusterCapacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.AllocatablePods != 8 || capacity.Pods != 3 || capacity.FreePods() != 5 {
		t.Fatalf("cluster pods = %d of %d (free %d), want 3 of 8 (free 5)", capacity.Pods, capacity.AllocatablePods, capacity.FreePods())
	}
	n1 := capacity.Nodes[0]
	if n1.AllocatablePods != 5 || n1.Pods != 2 || !n1.HasPodSlots(3) || n1.HasPodSlots(4) {
		t.Fatalf("n1 = %d of %d, want 2 of 5 holding 3 more but not 4", n1.Pods, n1.AllocatablePods)
	}
}

// A node that reports no pod limit (a mock, a Docker host) is not a full one.
func TestNodeCapacity_UnknownPodLimitHasRoom(t *testing.T) {
	if !(NodeCapacity{Pods: 500}).HasPodSlots(10) {
		t.Fatal("an unreported pod limit must not refuse")
	}
	if (ClusterCapacity{}).PodSlotsKnown() {
		t.Fatal("no allocatable pods means the limit is unknown")
	}
}
