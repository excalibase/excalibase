package k8s

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PendingPod is a pod the cluster has not started, and since when.
type PendingPod struct {
	Namespace string
	Name      string
	Since     time.Time
}

// PendingPods lists every pod in the Pending phase, in all namespaces (EXC-747).
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
			if pod.Status.Phase == corev1.PodPending {
				pending = append(pending, PendingPod{Namespace: pod.Namespace, Name: pod.Name, Since: pod.CreationTimestamp.Time})
			}
		}
		if pods.Continue == "" {
			return pending, nil
		}
		opts.Continue = pods.Continue
	}
}
