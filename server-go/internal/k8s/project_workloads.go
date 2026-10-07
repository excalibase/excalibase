package k8s

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// projectAppsSelector matches what the platform rendered for any app of a namespace.
const projectAppsSelector = "excalibase.io/app,app.kubernetes.io/managed-by=" + appManagedByValue

// WithdrawProjectWorkloads takes every app of a project off the edge (its own
// host and its custom domains) and scales every app and the function runtime
// to zero, remembering the counts. Disks, secrets and certificates stay, so a
// cancelled deletion can bring the project back. It does not wait for pods.
func (c *Client) WithdrawProjectWorkloads(ctx context.Context, namespace string) error {
	ingresses := c.clientset.NetworkingV1().Ingresses(namespace)
	routes, err := ingresses.List(ctx, metav1.ListOptions{LabelSelector: projectAppsSelector})
	if err != nil {
		return fmt.Errorf("list app routes: %w", err)
	}
	for _, name := range namesOf(routes.Items) {
		if err := ingresses.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete app route %s: %w", name, err)
		}
	}
	apps, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: projectAppsSelector})
	if err != nil {
		return fmt.Errorf("list app deployments: %w", err)
	}
	for _, name := range append(namesOf(apps.Items), denoRuntimeName) {
		err := c.updateAppDeployment(ctx, namespace, name, pauseDeployment)
		if err != nil && !errors.Is(err, ErrAppNotDeployed) {
			return err
		}
	}
	return nil
}

// RestartFunctionRuntime brings back a function runtime WithdrawProjectWorkloads
// stopped. A runtime that is missing or was not stopped is left as it is.
func (c *Client) RestartFunctionRuntime(ctx context.Context, namespace string) error {
	err := c.updateAppDeployment(ctx, namespace, denoRuntimeName, restoreStoppedReplicas)
	if errors.Is(err, ErrAppNotDeployed) {
		return nil
	}
	return err
}

func restoreStoppedReplicas(dep *appsv1.Deployment) error {
	replicas, err := pausedReplicas(dep)
	if errors.Is(err, ErrAppNotPaused) {
		return nil
	}
	if err != nil {
		return err
	}
	dep.Spec.Replicas = &replicas
	delete(dep.Annotations, appPausedReplicasAnnotation)
	return nil
}

// RestoreAppRoute serves a public app at its host again, with its certificate
// when it has one; an internal service has no route.
func (c *Client) RestoreAppRoute(ctx context.Context, namespace string, app *apphost.App, opts AppRouteOptions) error {
	route, err := buildAppRoute(namespace, app, opts)
	if err != nil {
		return err
	}
	if route.ingress == nil {
		return nil
	}
	return c.applyAppHostRoute(ctx, namespace, &AppWorkload{
		Ingress: route.ingress, HostCertificate: route.certificate,
		AcmeSolverPolicy: route.solverPolicy, dropHostCertificate: route.dropCertificate,
	})
}
