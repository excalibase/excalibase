package k8s

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AppRestarts is how often the app's current containers restarted and why the
// newest one stopped (OOMKilled, Error, ...). A new deploy's pods start at zero.
type AppRestarts struct {
	Count        int32      `json:"count"`
	LastReason   string     `json:"lastReason,omitempty"`
	LastExitCode *int32     `json:"lastExitCode,omitempty"`
	LastAt       *time.Time `json:"lastAt,omitempty"`
}

// AppRestarts reads the app's live pods (EXC-651).
func (c *Client) AppRestarts(ctx context.Context, namespace, appID string) (AppRestarts, error) {
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "excalibase.io/app=" + appID + ",app.kubernetes.io/managed-by=" + appManagedByValue,
	})
	if err != nil {
		return AppRestarts{}, fmt.Errorf("list app pods: %w", err)
	}
	var restarts AppRestarts
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			restarts.Count += status.RestartCount
			restarts.noteTermination(status.LastTerminationState.Terminated)
			restarts.noteTermination(status.State.Terminated)
		}
	}
	return restarts, nil
}

// noteTermination keeps the newest termination seen.
func (r *AppRestarts) noteTermination(terminated *corev1.ContainerStateTerminated) {
	if terminated == nil {
		return
	}
	at := terminated.FinishedAt.Time
	if r.LastAt != nil && !at.After(*r.LastAt) {
		return
	}
	exitCode := terminated.ExitCode
	r.LastReason, r.LastExitCode, r.LastAt = terminated.Reason, &exitCode, &at
}
