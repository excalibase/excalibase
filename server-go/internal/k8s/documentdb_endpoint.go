package k8s

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	return gatewayServing(p), nil
}

// ErrDocumentDBGatewayNotReady is returned when no primary pod has a serving
// gateway to dial.
var ErrDocumentDBGatewayNotReady = errors.New("the DocumentDB gateway is not serving")

// DocumentDBGatewayAddress returns the pod IP of the primary whose gateway is
// serving. The read-write Service forwards only Postgres's port, so the
// gateway is reached on the pod the Service selects, which follows failover.
func (c *Client) DocumentDBGatewayAddress(ctx context.Context, namespace, readWriteService string) (string, error) {
	selector, err := c.readWriteSelector(ctx, namespace, readWriteService)
	if err != nil {
		return "", err
	}
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(selector).String(),
	})
	if err != nil {
		return "", fmt.Errorf("list primary pods in %s: %w", namespace, err)
	}
	for i := range pods.Items {
		if pod := &pods.Items[i]; pod.Status.PodIP != "" && gatewayServing(pod) {
			return pod.Status.PodIP, nil
		}
	}
	return "", ErrDocumentDBGatewayNotReady
}

func gatewayServing(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == DocumentDBGatewayContainer {
			return status.Ready
		}
	}
	return false
}

// DocumentDBServiceName is the in-cluster Service for a project's gateway; the
// operator's read-write Service carries only the Postgres port.
func DocumentDBServiceName(projectID string) string {
	return projectID + "-documentdb"
}

// DocumentDBServiceHost is the name an in-cluster client dials for the gateway.
func DocumentDBServiceHost(projectID, namespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", DocumentDBServiceName(projectID), namespace)
}

// EnsureDocumentDBService creates or re-renders the gateway's ClusterIP
// Service. The selector is copied from the read-write Service so it follows
// whichever pod the operator labels primary.
func (c *Client) EnsureDocumentDBService(ctx context.Context, namespace, projectID string) error {
	selector, err := c.readWriteSelector(ctx, namespace, projectID+postgresSuffix+"-rw")
	if err != nil {
		return err
	}
	desired := buildDocumentDBService(namespace, projectID, selector)
	services := c.clientset.CoreV1().Services(namespace)
	existing, err := services.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := services.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create documentdb service: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read documentdb service: %w", err)
	}
	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec.Selector = desired.Spec.Selector
	updated.Spec.Ports = desired.Spec.Ports
	if _, err := services.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update documentdb service: %w", err)
	}
	return nil
}

func buildDocumentDBService(namespace, projectID string, selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DocumentDBServiceName(projectID),
			Namespace: namespace,
			Labels: map[string]string{
				dbEndpointManagedByLabel: dbEndpointManagedByValue,
				dbEndpointProjectLabel:   projectID,
			},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
			Ports: []corev1.ServicePort{{
				Name:       "documentdb",
				Port:       config.DocumentDBGatewayPort,
				TargetPort: intstr.FromInt(config.DocumentDBGatewayPort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}
