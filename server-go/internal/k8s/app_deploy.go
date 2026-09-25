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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	appRolloutPollInterval = 2 * time.Second
	crashLoopRestartLimit  = 3
	unschedulableGrace     = 30 * time.Second
)

var ErrAppRollout = fmt.Errorf("app rollout")

// ApplyAppWorkload fences the pods before they exist, so no app pod ever runs unfenced.
func (c *Client) ApplyAppWorkload(ctx context.Context, namespace string, workload *AppWorkload) error {
	if !workload.complete() {
		return fmt.Errorf("apply app workload: nothing rendered")
	}
	if err := c.applyAppPolicy(ctx, namespace, workload.EgressPolicy, "egress"); err != nil {
		return err
	}
	if err := c.applyAppPolicy(ctx, namespace, workload.IngressPolicy, "ingress"); err != nil {
		return err
	}
	if err := c.applyAppDeployment(ctx, namespace, workload.Deployment); err != nil {
		return err
	}
	if err := c.applyAppService(ctx, namespace, workload.Service); err != nil {
		return err
	}
	return c.applyAppIngress(ctx, namespace, workload.Ingress)
}

func (w *AppWorkload) complete() bool {
	return w != nil && w.Deployment != nil && w.EgressPolicy != nil &&
		w.Service != nil && w.Ingress != nil && w.IngressPolicy != nil
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

// applyAppService keeps the cluster IP the API server assigned: it is immutable.
func (c *Client) applyAppService(ctx context.Context, namespace string, desired *corev1.Service) error {
	services := c.clientset.CoreV1().Services(namespace)
	existing, err := services.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := services.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create app service: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read app service: %w", err)
	}
	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec.Type = desired.Spec.Type
	updated.Spec.Selector = desired.Spec.Selector
	updated.Spec.Ports = desired.Spec.Ports
	if _, err := services.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update app service: %w", err)
	}
	return nil
}

func (c *Client) applyAppIngress(ctx context.Context, namespace string, desired *networkingv1.Ingress) error {
	ingresses := c.clientset.NetworkingV1().Ingresses(namespace)
	existing, err := ingresses.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := ingresses.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create app ingress: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read app ingress: %w", err)
	}
	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec = desired.Spec
	if _, err := ingresses.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update app ingress: %w", err)
	}
	return nil
}

// A cluster without Cilium has no such kind, so the apply fails instead of leaving the app unfenced.
func (c *Client) applyAppPolicy(ctx context.Context, namespace string, desired *unstructured.Unstructured, direction string) error {
	return c.applyCiliumPolicy(ctx, namespace, desired, "app "+direction+" policy")
}

func (c *Client) applyCiliumPolicy(ctx context.Context, namespace string, desired *unstructured.Unstructured, what string) error {
	policies := c.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(namespace)
	existing, err := policies.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := policies.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create %s: %w", what, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", what, err)
	}
	updated := existing.DeepCopy()
	updated.SetLabels(desired.GetLabels())
	updated.Object["spec"] = runtime.DeepCopyJSONValue(desired.Object["spec"])
	if _, err := policies.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update %s: %w", what, err)
	}
	return nil
}

// These reasons never resolve on their own, so they fail the wait immediately
// instead of waiting out the full timeout.
var badImageReasons = map[string]bool{
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
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
	runtimeClass := ""
	if dep.Spec.Template.Spec.RuntimeClassName != nil {
		runtimeClass = *dep.Spec.Template.Spec.RuntimeClassName
	}
	now := time.Now()
	for _, pod := range pods.Items {
		if !hasOwner(pod.OwnerReferences, rs.UID) {
			continue
		}
		if reason, message, bad := podProblem(pod, runtimeClass, now); bad {
			return reason, message, true
		}
	}
	return "", "", false
}

func podProblem(pod corev1.Pod, runtimeClass string, now time.Time) (reason, message string, bad bool) {
	if detail, stuck := unschedulable(pod, now); stuck {
		return corev1.PodReasonUnschedulable,
			fmt.Sprintf("no node can run runtime class %q: %s", runtimeClass, detail), true
	}
	for _, status := range pod.Status.ContainerStatuses {
		if r, ok := waitingReason(status); ok {
			return r, status.State.Waiting.Message, true
		}
	}
	return "", "", false
}

// unschedulable waits out a grace period so an ordinary scheduling delay is not a failure.
func unschedulable(pod corev1.Pod, now time.Time) (string, bool) {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled &&
			condition.Status == corev1.ConditionFalse &&
			condition.Reason == corev1.PodReasonUnschedulable &&
			now.Sub(condition.LastTransitionTime.Time) >= unschedulableGrace {
			return condition.Message, true
		}
	}
	return "", false
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
	waiting := status.State.Waiting
	if waiting == nil {
		return "", false
	}
	if badImageReasons[waiting.Reason] ||
		(waiting.Reason == "CrashLoopBackOff" && status.RestartCount >= crashLoopRestartLimit) {
		return waiting.Reason, true
	}
	return "", false
}
