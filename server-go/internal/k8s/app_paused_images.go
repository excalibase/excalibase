package k8s

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// pausedDeployment reads the Deployment under the app's current name, once the app is known to have a workload.
func (c *Client) pausedDeployment(ctx context.Context, namespace, appID, appName string) (*appsv1.Deployment, error) {
	if _, err := c.appDeploymentNames(ctx, namespace, appID); err != nil {
		return nil, err
	}
	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, AppObjectName(appName), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, ErrAppNotDeployed
	}
	if err != nil {
		return nil, fmt.Errorf("read app deployment: %w", err)
	}
	return dep, nil
}

// PausedAppImages reads the images a resume would run from the paused
// Deployment's own pod template, init containers included: that, not the
// app's record, is what comes back up.
func (c *Client) PausedAppImages(ctx context.Context, namespace, appID, appName string) ([]string, error) {
	dep, err := c.pausedDeployment(ctx, namespace, appID, appName)
	if err != nil {
		return nil, err
	}
	if _, err := pausedReplicas(dep); err != nil {
		return nil, err
	}
	return templateImages(dep)
}

func templateImages(dep *appsv1.Deployment) ([]string, error) {
	pod := dep.Spec.Template.Spec
	if len(pod.Containers) == 0 {
		return nil, ErrAppImageUnreadable
	}
	var images []string
	for _, each := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
		if strings.TrimSpace(each.Image) == "" {
			return nil, ErrAppImageUnreadable
		}
		images = append(images, each.Image)
	}
	return images, nil
}
