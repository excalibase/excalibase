package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PausedAppImages reads the images a resume would run from the paused
// Deployment's own pod template, init containers included: that, not the
// app's record, is what comes back up.
func (c *Client) PausedAppImages(ctx context.Context, namespace, appID, appName string) ([]string, error) {
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
	if _, err := pausedReplicas(dep); err != nil {
		return nil, err
	}
	return templateImages(dep)
}

func templateImages(dep *appsv1.Deployment) ([]string, error) {
	pod := dep.Spec.Template.Spec
	var images []string
	for _, each := range pod.InitContainers {
		images = append(images, each.Image)
	}
	for _, each := range pod.Containers {
		images = append(images, each.Image)
	}
	if len(pod.Containers) == 0 {
		return nil, errors.New("the app's paused deployment names no container")
	}
	for _, image := range images {
		if strings.TrimSpace(image) == "" {
			return nil, errors.New("the app's paused deployment has a container with no image")
		}
	}
	return images, nil
}
