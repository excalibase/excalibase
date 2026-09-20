package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DocumentDBGatewayContainer is the name the CNPG-I sidecar injector gives the
// gateway container it adds to a DocumentDB project's Postgres pod. It is
// upstream's name, and it is what this platform looks for when asked whether a
// project's Mongo endpoint is really serving.
const DocumentDBGatewayContainer = "documentdb-gateway"

// DocumentDBGatewayReady reports whether the project's gateway container is
// serving (EXC-409).
//
// It is asked about the container rather than the pod on purpose. The gateway
// starts by waiting for Postgres to accept connections and then creating the
// Mongo user, so there is a real window in which the pod is Running and
// Postgres is answering while the gateway is not — and a customer told in that
// window that their Mongo endpoint is up gets a refused connection. The same
// applies in reverse to a pod carrying no gateway container at all: it is not
// a DocumentDB pod, and the honest answer is no.
//
// A pod that is not there is not ready and not an error: that is what a
// paused, deleting or not-yet-started project looks like.
func (c *Client) DocumentDBGatewayReady(ctx context.Context, namespace, pod string) (bool, error) {
	p, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read pod %s/%s: %w", namespace, pod, err)
	}
	if p.Status.Phase != corev1.PodRunning {
		return false, nil
	}
	for _, status := range p.Status.ContainerStatuses {
		if status.Name == DocumentDBGatewayContainer {
			return status.Ready, nil
		}
	}
	return false, nil
}
