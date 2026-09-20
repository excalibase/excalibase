package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// A project's public database endpoint is one Kubernetes Service of type
// LoadBalancer (EXC-410). Several such Services carry MetalLB's shared-IP
// annotation with a common key, so they all land on one public address and
// take a port each; Kubernetes then does the routing and there is no edge
// process of ours to configure, reload or keep alive.
//
// TLS is not terminated here. The session is encrypted all the way to
// Postgres, so nothing at the edge ever holds a tenant's credentials — which
// is also why this Service needs no secret of any kind.

const (
	// metalLBSharedIPAnnotation is what puts several LoadBalancer Services
	// on one address. MetalLB grants the same IP to every Service whose
	// value matches (and whose ports do not collide, which the port
	// allocator guarantees).
	metalLBSharedIPAnnotation = "metallb.universe.tf/allow-shared-ip"

	// postgresPort is the port inside the pod. Only the published port
	// varies per project; the database always listens on 5432.
	postgresPort = 5432

	// dbEndpointManagedByLabel marks the Services this platform owns, so an
	// operator can tell ours from the ones the CNPG operator creates.
	dbEndpointManagedByLabel = "app.kubernetes.io/managed-by"
	dbEndpointProjectLabel   = "excalibase.io/project"
	dbEndpointManagedByValue = "excalibase-provisioning"
)

// PublicDBServiceSpec describes one project's public database endpoint.
//
// ReadWriteService names the CNPG-owned Service for the project's cluster.
// Its selector is copied rather than reconstructed: the operator decides
// which labels mark the primary and has changed them between releases, so
// reading the selector it actually wrote is the only way to be sure the
// public port reaches the primary and not a replica or nothing at all.
type PublicDBServiceSpec struct {
	Name             string // the Service to create, domain.DBEndpointServiceName
	Port             int    // the public TCP port this project holds
	ReadWriteService string // the CNPG read-write Service to copy the selector from
	SharedIPKey      string // MetalLB sharing key, common to every project
	ProjectID        string
	// TargetPort is the port inside the pod. Zero means Postgres, which is
	// every project. A DocumentDB project has a second Service pointed at the
	// gateway's port instead (EXC-409) — the same pod and therefore the same
	// selector, because the gateway is a container beside Postgres rather
	// than a workload of its own.
	TargetPort int
	// PortName names the port on the Service. Empty means "postgres".
	PortName string
}

// EnsurePublicDBService creates the project's public LoadBalancer Service, or
// re-renders an existing one onto the port the project currently holds.
// Idempotent, so a retried enable and a resume after a pause both converge.
func (c *Client) EnsurePublicDBService(ctx context.Context, namespace string, spec PublicDBServiceSpec) error {
	selector, err := c.readWriteSelector(ctx, namespace, spec.ReadWriteService)
	if err != nil {
		return err
	}
	services := c.clientset.CoreV1().Services(namespace)
	desired := buildPublicDBService(namespace, spec, selector)

	existing, err := services.Get(ctx, spec.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := services.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create public database service: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read public database service: %w", err)
	}

	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Annotations = desired.Annotations
	updated.Spec.Type = desired.Spec.Type
	updated.Spec.Selector = desired.Spec.Selector
	updated.Spec.Ports = desired.Spec.Ports
	if _, err := services.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update public database service: %w", err)
	}
	return nil
}

// PublicDBServiceExists reports whether the project's Service is present. A
// project with no Service refuses connections, which is the honest answer for
// one that is paused, being deleted or restoring.
func (c *Client) PublicDBServiceExists(ctx context.Context, namespace, name string) (bool, error) {
	_, err := c.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check public database service: %w", err)
	}
	return true, nil
}

// DeletePublicDBService removes the project's Service so the port stops
// answering. Deleting one that is already gone succeeds: a teardown or a
// pause may be retried.
func (c *Client) DeletePublicDBService(ctx context.Context, namespace, name string) error {
	err := c.clientset.CoreV1().Services(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete public database service: %w", err)
	}
	return nil
}

// readWriteSelector returns the selector the CNPG operator put on the
// cluster's read-write Service. A missing Service is refused rather than
// guessed around: a LoadBalancer with an invented selector would publish a
// port that reaches nothing, or worse, the wrong pod.
func (c *Client) readWriteSelector(ctx context.Context, namespace, name string) (map[string]string, error) {
	svc, err := c.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read cluster read-write service %s/%s: %w", namespace, name, err)
	}
	if len(svc.Spec.Selector) == 0 {
		return nil, fmt.Errorf("cluster read-write service %s/%s has no selector to copy", namespace, name)
	}
	selector := make(map[string]string, len(svc.Spec.Selector))
	for key, value := range svc.Spec.Selector {
		selector[key] = value
	}
	return selector, nil
}

// serviceTargetPort is the port inside the pod this Service forwards to.
func serviceTargetPort(spec PublicDBServiceSpec) int {
	if spec.TargetPort > 0 {
		return spec.TargetPort
	}
	return postgresPort
}

// servicePortName names the port, so an operator reading the Service can see
// which protocol it carries.
func servicePortName(spec PublicDBServiceSpec) string {
	if spec.PortName != "" {
		return spec.PortName
	}
	return "postgres"
}

func buildPublicDBService(namespace string, spec PublicDBServiceSpec, selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: namespace,
			Labels: map[string]string{
				dbEndpointManagedByLabel: dbEndpointManagedByValue,
				dbEndpointProjectLabel:   spec.ProjectID,
			},
			Annotations: map[string]string{
				metalLBSharedIPAnnotation: spec.SharedIPKey,
			},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeLoadBalancer,
			Selector: selector,
			Ports: []corev1.ServicePort{{
				Name:       servicePortName(spec),
				Port:       int32(spec.Port),
				TargetPort: intstr.FromInt(serviceTargetPort(spec)),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}
