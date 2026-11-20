package k8s

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PendingPod is a pod the scheduler could not place, and since when.
type PendingPod struct {
	Namespace string
	Name      string
	Since     time.Time
}

// PendingPods lists the pods the scheduler reported Unschedulable, in all
// namespaces (EXC-747). A pod that is Pending for another reason (an image that
// will not pull, an init container still running) was scheduled, so it is no
// sign that the cluster is full.
func (c *Client) PendingPods(ctx context.Context) ([]PendingPod, error) {
	var pending []PendingPod
	opts := metav1.ListOptions{FieldSelector: "status.phase=" + string(corev1.PodPending), Limit: podListPage}
	for {
		pods, err := c.clientset.CoreV1().Pods("").List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("list pending pods: %w", err)
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			// The fake clientset ignores field selectors, so the phase is checked again.
			if pod.Status.Phase == corev1.PodPending && schedulerRefused(pod) {
				pending = append(pending, PendingPod{Namespace: pod.Namespace, Name: pod.Name, Since: pod.CreationTimestamp.Time})
			}
		}
		if pods.Continue == "" {
			return pending, nil
		}
		opts.Continue = pods.Continue
	}
}

func schedulerRefused(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled {
			return condition.Status == corev1.ConditionFalse && condition.Reason == corev1.PodReasonUnschedulable
		}
	}
	return false
}
