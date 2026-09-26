package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LiveAppPods sums what the app's pods a rollout will replace request now:
// running or pending and not already on their way out.
func (c *Client) LiveAppPods(ctx context.Context, namespace, appID string) (AppPods, error) {
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "excalibase.io/app=" + appID + ",app.kubernetes.io/managed-by=" + appManagedByValue,
	})
	if err != nil {
		return AppPods{}, fmt.Errorf("list app pods: %w", err)
	}
	var live AppPods
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		cpu, mem := podRequest(pod)
		live.Count++
		live.CPUMilli += cpu
		live.MemBytes += mem
		live.MaxCPUMilli = max(live.MaxCPUMilli, cpu)
		live.MaxMemBytes = max(live.MaxMemBytes, mem)
	}
	return live, nil
}

// RuntimeClassPlacement reads what the scheduler adds to every pod of the class and the nodes it may use.
func (c *Client) RuntimeClassPlacement(ctx context.Context, name string) (RuntimePlacement, error) {
	class, err := c.clientset.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return RuntimePlacement{}, fmt.Errorf("read runtime class %q: %w", name, err)
	}
	var placement RuntimePlacement
	if class.Overhead != nil {
		placement.OverheadCPUMilli = class.Overhead.PodFixed.Cpu().MilliValue()
		placement.OverheadMemBytes = class.Overhead.PodFixed.Memory().Value()
	}
	if class.Scheduling != nil {
		placement.NodeSelector = class.Scheduling.NodeSelector
	}
	return placement, nil
}
