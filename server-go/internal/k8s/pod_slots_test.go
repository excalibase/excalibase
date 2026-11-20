package k8s

import (
	"context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
	"time"

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

func scheduledCondition(status corev1.ConditionStatus, reason string) corev1.PodCondition {
	return corev1.PodCondition{Type: corev1.PodScheduled, Status: status, Reason: reason}
}

// Only a pod the scheduler could not place counts: one customer's ImagePullBackOff is Pending too, and must not hold the alert open.
func TestPendingPods_ListsOnlyUnschedulablePodsWithTheirAge(t *testing.T) {
	waiting := podOn("waiting", "", corev1.PodPending)
	waiting.CreationTimestamp = metav1.NewTime(time.Unix(1000, 0))
	waiting.Status.Conditions = []corev1.PodCondition{scheduledCondition(corev1.ConditionFalse, corev1.PodReasonUnschedulable)}
	pulling := podOn("bad-image", "n1", corev1.PodPending)
	pulling.Status.Conditions = []corev1.PodCondition{scheduledCondition(corev1.ConditionTrue, "")}
	fresh := podOn("not-yet-looked-at", "", corev1.PodPending)
	c := newFakeClient(waiting, pulling, fresh, podOn("running", "n1", corev1.PodRunning))
	got, err := c.PendingPods(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "waiting" || got[0].Namespace != "ns" || !got[0].Since.Equal(time.Unix(1000, 0)) {
		t.Fatalf("pending = %+v, want only the unschedulable pod", got)
	}
}
