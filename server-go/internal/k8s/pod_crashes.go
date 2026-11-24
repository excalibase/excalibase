package k8s

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The annotations a suspended app's Deployment carries (EXC-839).
const (
	AppSuspendedAnnotation       = "excalibase.io/suspended"
	AppSuspendedReasonAnnotation = "excalibase.io/suspended-reason"
	AppSuspendedAtAnnotation     = "excalibase.io/suspended-at"
)

// ContainerExit is how a container's last run ended.
type ContainerExit struct {
	Reason     string
	ExitCode   int32
	FinishedAt time.Time
}

// ContainerCrash is one container of one pod as the crash-loop guard reads it:
// how often it has restarted and how its last run ended. A pod that is being
// deleted is marked, because its containers' exits belong to a rollout.
type ContainerCrash struct {
	PodUID       string
	PodName      string
	Container    string
	RestartCount int32
	LastExit     *ContainerExit
	Deleting     bool
}

// AppContainerStates reads the containers of the app's pods.
func (c *Client) AppContainerStates(ctx context.Context, namespace, appID string) ([]ContainerCrash, error) {
	return c.containerStates(ctx, namespace, appOwnedSelector(appID))
}

// databaseSelectors match a CNPG cluster's pods and a MOCO MySQL cluster's pods.
var databaseSelectors = []string{"cnpg.io/cluster", mocoCreatedByLabel + "=moco"}

// DatabaseContainerStates reads the containers of the project's database pods.
func (c *Client) DatabaseContainerStates(ctx context.Context, namespace string) ([]ContainerCrash, error) {
	var all []ContainerCrash
	for _, selector := range databaseSelectors {
		states, err := c.containerStates(ctx, namespace, selector)
		if err != nil {
			return nil, err
		}
		all = append(all, states...)
	}
	return all, nil
}

func (c *Client) containerStates(ctx context.Context, namespace, selector string) ([]ContainerCrash, error) {
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list pods %s: %w", selector, err)
	}
	var states []ContainerCrash
	for i := range pods.Items {
		pod := &pods.Items[i]
		for _, status := range pod.Status.ContainerStatuses {
			states = append(states, ContainerCrash{
				PodUID: string(pod.UID), PodName: pod.Name, Container: status.Name,
				RestartCount: status.RestartCount, LastExit: lastExitOf(status),
				Deleting: pod.DeletionTimestamp != nil,
			})
		}
	}
	return states, nil
}

func lastExitOf(status corev1.ContainerStatus) *ContainerExit {
	terminated := status.LastTerminationState.Terminated
	if terminated == nil {
		return nil
	}
	return &ContainerExit{Reason: terminated.Reason, ExitCode: terminated.ExitCode, FinishedAt: terminated.FinishedAt.Time}
}

// SuspendAppWorkload marks the app's Deployments as stopped on purpose, with
// why and when, beside the paused-replicas marker a pause leaves.
func (c *Client) SuspendAppWorkload(ctx context.Context, namespace, appID, kind, reason string, at time.Time) error {
	names, err := c.appDeploymentNames(ctx, namespace, appID)
	if err != nil {
		return err
	}
	for _, name := range names {
		err := c.updateAppDeployment(ctx, namespace, name, func(dep *appsv1.Deployment) error {
			if dep.Annotations == nil {
				dep.Annotations = map[string]string{}
			}
			dep.Annotations[AppSuspendedAnnotation] = kind
			dep.Annotations[AppSuspendedReasonAnnotation] = reason
			dep.Annotations[AppSuspendedAtAnnotation] = at.UTC().Format(time.RFC3339)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func clearSuspension(dep *appsv1.Deployment) {
	delete(dep.Annotations, AppSuspendedAnnotation)
	delete(dep.Annotations, AppSuspendedReasonAnnotation)
	delete(dep.Annotations, AppSuspendedAtAnnotation)
}
