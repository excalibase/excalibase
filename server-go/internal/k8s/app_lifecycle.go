package k8s

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

// appPausedReplicasAnnotation is the replica count a resume restores; only a pause writes it.
const appPausedReplicasAnnotation = "excalibase.io/paused-replicas"

var (
	ErrAppNotDeployed = errors.New("the app has no workload in the cluster")
	ErrAppNotPaused   = errors.New("the app is not paused")
	ErrAppPodsRemain  = errors.New("the app's workload is still running")
)

// appOwnedSelector matches everything the platform rendered for one app, whatever it was named at the time.
func appOwnedSelector(appID string) string {
	return "excalibase.io/app=" + appID + ",app.kubernetes.io/managed-by=" + appManagedByValue
}

// PauseAppWorkload scales every Deployment of the app to zero and keeps
// everything else, so a resume brings back exactly this workload. Found by
// label, so a Deployment left under an earlier name is stopped too.
func (c *Client) PauseAppWorkload(ctx context.Context, namespace, appID string) error {
	names, err := c.appDeploymentNames(ctx, namespace, appID)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := c.updateAppDeployment(ctx, namespace, name, pauseDeployment); err != nil {
			return err
		}
	}
	return nil
}

func pauseDeployment(dep *appsv1.Deployment) error {
	if _, paused := dep.Annotations[appPausedReplicasAnnotation]; !paused && replicasOf(dep) > 0 {
		if dep.Annotations == nil {
			dep.Annotations = map[string]string{}
		}
		dep.Annotations[appPausedReplicasAnnotation] = strconv.Itoa(int(replicasOf(dep)))
	}
	zero := int32(0)
	dep.Spec.Replicas = &zero
	return nil
}

// ResumeAppWorkload restores the paused replica count of the Deployment under
// the app's current name, at the pod size of the plan given, and waits for it
// to be ready; one left under an earlier name stays stopped. The marker stays
// until ready, so a failed resume can be retried.
func (c *Client) ResumeAppWorkload(ctx context.Context, namespace, appID, appName string, tier domain.TierType, timeout time.Duration) error {
	size, err := config.GetAppTierConfig(tier)
	if err != nil {
		return err
	}
	resources, err := appResourceRequirements(size)
	if err != nil {
		return err
	}
	if _, err := c.appDeploymentNames(ctx, namespace, appID); err != nil {
		return err
	}
	return c.resumeDeployment(ctx, namespace, AppObjectName(appName), resources, timeout)
}

// PausedAppReplicas reads the replica count a resume would restore, so it can be admitted first.
func (c *Client) PausedAppReplicas(ctx context.Context, namespace, appID, appName string) (int, error) {
	if _, err := c.appDeploymentNames(ctx, namespace, appID); err != nil {
		return 0, err
	}
	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, AppObjectName(appName), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return 0, ErrAppNotDeployed
	}
	if err != nil {
		return 0, fmt.Errorf("read app deployment: %w", err)
	}
	replicas, err := pausedReplicas(dep)
	return int(replicas), err
}

func (c *Client) resumeDeployment(ctx context.Context, namespace, name string, resources corev1.ResourceRequirements, timeout time.Duration) error {
	err := c.updateAppDeployment(ctx, namespace, name, func(dep *appsv1.Deployment) error {
		replicas, err := pausedReplicas(dep)
		if err != nil {
			return err
		}
		dep.Spec.Replicas = &replicas
		for i := range dep.Spec.Template.Spec.Containers {
			dep.Spec.Template.Spec.Containers[i].Resources = *resources.DeepCopy()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.waitForApp(ctx, namespace, name, "", timeout); err != nil {
		return err
	}
	return c.updateAppDeployment(ctx, namespace, name, func(dep *appsv1.Deployment) error {
		delete(dep.Annotations, appPausedReplicasAnnotation)
		return nil
	})
}

func (c *Client) appDeploymentNames(ctx context.Context, namespace, appID string) ([]string, error) {
	list, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: appOwnedSelector(appID)})
	if err != nil {
		return nil, fmt.Errorf("list app deployments: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, ErrAppNotDeployed
	}
	return namesOf(list.Items), nil
}

func pausedReplicas(dep *appsv1.Deployment) (int32, error) {
	raw, paused := dep.Annotations[appPausedReplicasAnnotation]
	if !paused {
		return 0, ErrAppNotPaused
	}
	replicas, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || replicas < 1 {
		return 0, fmt.Errorf("the app's paused replica count %q is unreadable", raw)
	}
	return int32(replicas), nil
}

func replicasOf(dep *appsv1.Deployment) int32 {
	if dep.Spec.Replicas == nil {
		return 1
	}
	return *dep.Spec.Replicas
}

func (c *Client) updateAppDeployment(ctx context.Context, namespace, name string, change func(*appsv1.Deployment) error) error {
	deployments := c.clientset.AppsV1().Deployments(namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		dep, err := deployments.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return ErrAppNotDeployed
		}
		if err != nil {
			return fmt.Errorf("read app deployment: %w", err)
		}
		if err := change(dep); err != nil {
			return err
		}
		if _, err := deployments.Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update app deployment: %w", err)
		}
		return nil
	})
}

// WaitForAppPodsGone lists terminating pods too: a pod is gone only once the
// API server no longer has it, which is when it no longer holds its node's resources.
func (c *Client) WaitForAppPodsGone(ctx context.Context, namespace, appID string, timeout time.Duration) error {
	return c.waitForPodsGone(ctx, namespace, appOwnedSelector(appID), timeout)
}

func (c *Client) waitForPodsGone(ctx context.Context, namespace, selector string, timeout time.Duration) error {
	pods := c.clientset.CoreV1().Pods(namespace)
	return waitUntilNoneLeft(ctx, timeout, func(ctx context.Context) ([]string, error) {
		list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return nil, fmt.Errorf("list app pods: %w", err)
		}
		return namesOf(list.Items), nil
	})
}

func (c *Client) waitForReplicaSetsGone(ctx context.Context, namespace, selector string, timeout time.Duration) error {
	replicaSets := c.clientset.AppsV1().ReplicaSets(namespace)
	return waitUntilNoneLeft(ctx, timeout, func(ctx context.Context) ([]string, error) {
		list, err := replicaSets.List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return nil, fmt.Errorf("list app replica sets: %w", err)
		}
		return namesOf(list.Items), nil
	})
}

func waitUntilNoneLeft(ctx context.Context, timeout time.Duration, list func(context.Context) ([]string, error)) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var remaining []string
	err := wait.PollUntilContextCancel(waitCtx, appRolloutPollInterval, true, func(pollCtx context.Context) (bool, error) {
		names, err := list(pollCtx)
		if err != nil {
			return false, err
		}
		remaining = names
		return len(names) == 0, nil
	})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil || !errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w after %s: %s", ErrAppPodsRemain, timeout, strings.Join(remaining, ", "))
}

// DeleteAppWorkload takes the route away first and the network fence last, so
// no pod of the app is ever reachable or unfenced while it is still running.
// The app's disk goes last of all, once no pod can write to it.
func (c *Client) DeleteAppWorkload(ctx context.Context, namespace, appID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if err := c.deleteAppObjects(ctx, namespace, appOwnedSelector(appID), timeout); err != nil {
		return err
	}
	return c.deleteAppDisks(ctx, namespace, appOwnedSelector(appID), max(time.Until(deadline), time.Millisecond))
}

// PruneAppWorkload deletes, in the same order, whatever the app still runs
// under any name but keepName: a renamed app's old route, pods and secrets.
func (c *Client) PruneAppWorkload(ctx context.Context, namespace, appID, keepName string, timeout time.Duration) error {
	return c.deleteAppObjects(ctx, namespace, appOwnedSelector(appID)+",app.kubernetes.io/name!="+keepName, timeout)
}

func (c *Client) deleteAppObjects(ctx context.Context, namespace, selector string, timeout time.Duration) error {
	opts := metav1.ListOptions{LabelSelector: selector}
	background := metav1.DeletePropagationBackground
	ingresses := c.clientset.NetworkingV1().Ingresses(namespace)
	deployments := c.clientset.AppsV1().Deployments(namespace)
	steps := []ownedKind{
		{"ingress", func(ctx context.Context) ([]string, error) {
			list, err := ingresses.List(ctx, opts)
			if err != nil {
				return nil, err
			}
			return namesOf(list.Items), nil
		}, func(ctx context.Context, name string) error {
			return ingresses.Delete(ctx, name, metav1.DeleteOptions{})
		}},
		{"deployment", func(ctx context.Context) ([]string, error) {
			list, err := deployments.List(ctx, opts)
			if err != nil {
				return nil, err
			}
			return namesOf(list.Items), nil
		}, func(ctx context.Context, name string) error {
			return deployments.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &background})
		}},
	}
	if err := deleteOwned(ctx, steps); err != nil {
		return err
	}
	// One deadline covers both waits, so a deletion never takes twice its budget.
	deadline := time.Now().Add(timeout)
	if err := c.waitForPodsGone(ctx, namespace, selector, timeout); err != nil {
		return err
	}
	if err := c.waitForReplicaSetsGone(ctx, namespace, selector, max(time.Until(deadline), time.Millisecond)); err != nil {
		return err
	}
	return deleteOwned(ctx, c.appLeftovers(namespace, opts))
}

type ownedKind struct {
	what   string
	list   func(context.Context) ([]string, error)
	delete func(context.Context, string) error
}

func deleteOwned(ctx context.Context, kinds []ownedKind) error {
	for _, kind := range kinds {
		names, err := kind.list(ctx)
		if err != nil {
			return fmt.Errorf("list app %s: %w", kind.what, err)
		}
		for _, name := range names {
			if err := kind.delete(ctx, name); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete app %s %s: %w", kind.what, name, err)
			}
		}
	}
	return nil
}

func (c *Client) appLeftovers(namespace string, opts metav1.ListOptions) []ownedKind {
	services := c.clientset.CoreV1().Services(namespace)
	secrets := c.clientset.CoreV1().Secrets(namespace)
	policies := c.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(namespace)
	// A cluster without cert-manager has no certificates to list, and none to leave behind.
	certs := c.dynamicClient.Resource(CertificateGVR).Namespace(namespace)
	return []ownedKind{
		{"service", func(ctx context.Context) ([]string, error) {
			list, err := services.List(ctx, opts)
			if err != nil {
				return nil, err
			}
			return namesOf(list.Items), nil
		}, func(ctx context.Context, name string) error {
			return services.Delete(ctx, name, metav1.DeleteOptions{})
		}},
		{"secret", func(ctx context.Context) ([]string, error) {
			list, err := secrets.List(ctx, opts)
			if err != nil {
				return nil, err
			}
			return namesOf(list.Items), nil
		}, func(ctx context.Context, name string) error { return secrets.Delete(ctx, name, metav1.DeleteOptions{}) }},
		{"certificate", func(ctx context.Context) ([]string, error) {
			list, err := certs.List(ctx, opts)
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			return namesOf(list.Items), nil
		}, func(ctx context.Context, name string) error { return certs.Delete(ctx, name, metav1.DeleteOptions{}) }},
		{"network policy", func(ctx context.Context) ([]string, error) {
			list, err := policies.List(ctx, opts)
			if err != nil {
				return nil, err
			}
			return namesOf(list.Items), nil
		}, func(ctx context.Context, name string) error {
			return policies.Delete(ctx, name, metav1.DeleteOptions{})
		}},
	}
}

func namesOf[T any, P interface {
	*T
	metav1.Object
}](items []T) []string {
	names := make([]string, 0, len(items))
	for i := range items {
		names = append(names, P(&items[i]).GetName())
	}
	return names
}
