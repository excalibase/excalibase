package k8s

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const appRolloutPollInterval = 2 * time.Second

var ErrAppRollout = fmt.Errorf("app rollout")

func (c *Client) ApplyAppWorkload(ctx context.Context, namespace string, workload *AppWorkload) error {
	if workload == nil || workload.Deployment == nil || workload.NetworkPolicy == nil {
		return fmt.Errorf("apply app workload: nothing rendered")
	}
	if err := c.applyAppDeployment(ctx, namespace, workload.Deployment); err != nil {
		return err
	}
	return c.applyAppEgressPolicy(ctx, namespace, workload.NetworkPolicy)
}

func (c *Client) applyAppDeployment(ctx context.Context, namespace string, desired *appsv1.Deployment) error {
	deployments := c.clientset.AppsV1().Deployments(namespace)
	existing, err := deployments.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := deployments.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create app deployment: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read app deployment: %w", err)
	}
	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec = desired.Spec
	if _, err := deployments.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update app deployment: %w", err)
	}
	return nil
}

func (c *Client) applyAppEgressPolicy(ctx context.Context, namespace string, desired *networkingv1.NetworkPolicy) error {
	policies := c.clientset.NetworkingV1().NetworkPolicies(namespace)
	existing, err := policies.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := policies.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create app egress policy: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read app egress policy: %w", err)
	}
	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec = desired.Spec
	if _, err := policies.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update app egress policy: %w", err)
	}
	return nil
}

// These reasons never resolve on their own, so they fail the wait immediately
// instead of waiting out the full timeout. CreateContainerConfigError is how
// a root image is refused under runAsNonRoot.
var badImageReasons = map[string]bool{
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"CrashLoopBackOff":           true,
	"CreateContainerConfigError": true,
	"InvalidImageName":           true,
}

func (c *Client) WaitForAppRollout(ctx context.Context, namespace, name string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := wait.PollUntilContextCancel(waitCtx, appRolloutPollInterval, true, func(pollCtx context.Context) (bool, error) {
		dep, err := c.clientset.AppsV1().Deployments(namespace).Get(pollCtx, name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("read app deployment: %w", err)
		}
		if converged(dep) {
			return true, nil
		}
		// Until the controller has seen the new spec, the revision annotation
		// still names the previous ReplicaSet, whose pods may be failing.
		if dep.Status.ObservedGeneration < dep.Generation {
			return false, nil
		}
		if reason, message, bad := c.badPod(pollCtx, namespace, dep); bad {
			return false, fmt.Errorf("%w: %s %s: %s", ErrAppRollout, name, reason, message)
		}
		return false, nil
	})
	if err == nil {
		return nil
	}
	// The client's rate limiter reports the deadline in its own words, so ask
	// the context rather than the error.
	if ctx.Err() == nil && errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s did not become ready within %s", ErrAppRollout, name, timeout)
	}
	return err
}

func converged(dep *appsv1.Deployment) bool {
	if dep.Spec.Replicas == nil {
		return false
	}
	want := *dep.Spec.Replicas
	return dep.Status.ObservedGeneration >= dep.Generation &&
		dep.Status.UpdatedReplicas == want &&
		dep.Status.Replicas == want &&
		dep.Status.AvailableReplicas == want
}

const revisionAnnotation = "deployment.kubernetes.io/revision"

// Only inspects dep's current ReplicaSet's pods — a previous one's may still
// be crashing on the way out.
func (c *Client) badPod(ctx context.Context, namespace string, dep *appsv1.Deployment) (reason, message string, bad bool) {
	if dep.Spec.Selector == nil || len(dep.Spec.Selector.MatchLabels) == 0 {
		return "", "", false
	}
	rs := c.currentReplicaSet(ctx, namespace, dep)
	if rs == nil {
		return "", "", false
	}
	selector := labels.SelectorFromSet(dep.Spec.Selector.MatchLabels).String()
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return "", "", false
	}
	for _, pod := range pods.Items {
		if !hasOwner(pod.OwnerReferences, rs.UID) {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			if r, ok := waitingReason(status); ok {
				return r, status.State.Waiting.Message, true
			}
		}
	}
	return "", "", false
}

func (c *Client) currentReplicaSet(ctx context.Context, namespace string, dep *appsv1.Deployment) *appsv1.ReplicaSet {
	want := dep.Annotations[revisionAnnotation]
	if want == "" {
		return nil
	}
	list, err := c.clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	for i := range list.Items {
		rs := &list.Items[i]
		if hasOwner(rs.OwnerReferences, dep.UID) && rs.Annotations[revisionAnnotation] == want {
			return rs
		}
	}
	return nil
}

func hasOwner(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, ref := range refs {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

func waitingReason(status corev1.ContainerStatus) (string, bool) {
	if status.State.Waiting == nil {
		return "", false
	}
	if badImageReasons[status.State.Waiting.Reason] {
		return status.State.Waiting.Reason, true
	}
	return "", false
}
