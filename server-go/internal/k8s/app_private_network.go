package k8s

import (
	"context"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// AppPrivateNetworkPolicyName is the project's opt-in (EXC-524): while it
// exists, the namespace's apps reach each other; without it nothing changes.
const AppPrivateNetworkPolicyName = "apps-private-network"

const appPrivateNetworkComponent = "app-private-network"

// buildAppPrivateNetworkPolicy lets this namespace's app pods, and nothing
// else, reach each other's named ports. Named ports resolve on the receiving
// pod, so one static policy serves every app without per-app rendering; the
// database, other pods and other namespaces carry no app label and stay out.
func buildAppPrivateNetworkPolicy(namespace string) (*unstructured.Unstructured, error) {
	if !namespacePattern.MatchString(namespace) {
		return nil, fmt.Errorf("%w: %q is not a project namespace", ErrRenderApp, namespace)
	}
	sameProjectApps := metav1.LabelSelector{MatchLabels: map[string]string{
		componentLabelKey: appComponentLabel,
		podNamespaceKey:   namespace,
	}}
	spec := ciliumPolicySpec{
		EndpointSelector: metav1.LabelSelector{MatchLabels: map[string]string{componentLabelKey: appComponentLabel}},
		Ingress: []ciliumIngressRule{{
			FromEndpoints: []metav1.LabelSelector{sameProjectApps},
			ToPorts:       []ciliumPortRule{{Ports: appPrivatePorts()}},
		}},
		// The receiving app's ingress rule decides the port.
		Egress: []ciliumEgressRule{{ToEndpoints: []metav1.LabelSelector{sameProjectApps}}},
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
	if err != nil {
		return nil, fmt.Errorf("%w: policy %s: %w", ErrRenderApp, AppPrivateNetworkPolicyName, err)
	}
	policy := &unstructured.Unstructured{Object: map[string]interface{}{"spec": content}}
	policy.SetAPIVersion(CiliumNetworkPolicyGVR.GroupVersion().String())
	policy.SetKind(ciliumPolicyKind)
	policy.SetName(AppPrivateNetworkPolicyName)
	policy.SetNamespace(namespace)
	policy.SetLabels(map[string]string{
		componentLabelKey:              appPrivateNetworkComponent,
		"app.kubernetes.io/managed-by": appManagedByValue,
	})
	return policy, nil
}

// appPrivatePorts are the container port names an app may be reached on from
// its project: the HTTP port and every internal port slot (EXC-525).
func appPrivatePorts() []ciliumPort {
	ports := []ciliumPort{{Port: appServicePortName, Protocol: protocolTCP}}
	for slot := range apphost.MaxInternalPorts {
		ports = append(ports, ciliumPort{Port: internalPortName(slot), Protocol: protocolTCP})
	}
	return ports
}

// internalPortName names the app's slot-th internal port; slots, not numbers,
// so one static policy covers every app's ports.
func internalPortName(slot int) string { return "internal-" + strconv.Itoa(slot+1) }

// SetAppPrivateNetwork opens or closes app-to-app traffic in the namespace
// and reads the result back, so a caller records only what the cluster holds.
func (c *Client) SetAppPrivateNetwork(ctx context.Context, namespace string, open bool) error {
	if !open {
		return c.closeAppPrivateNetwork(ctx, namespace)
	}
	policy, err := buildAppPrivateNetworkPolicy(namespace)
	if err != nil {
		return err
	}
	if err := c.applyCiliumPolicy(ctx, namespace, policy, "app private network policy"); err != nil {
		return err
	}
	observed, err := c.AppPrivateNetworkOpen(ctx, namespace)
	if err != nil {
		return err
	}
	if !observed {
		return fmt.Errorf("app private network policy in %s was applied but is not there", namespace)
	}
	return nil
}

func (c *Client) closeAppPrivateNetwork(ctx context.Context, namespace string) error {
	policies := c.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(namespace)
	if err := policies.Delete(ctx, AppPrivateNetworkPolicyName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete app private network policy: %w", err)
	}
	observed, err := c.AppPrivateNetworkOpen(ctx, namespace)
	if err != nil {
		return err
	}
	if observed {
		return fmt.Errorf("app private network policy in %s is still there after its deletion", namespace)
	}
	return nil
}

// AppPrivateNetworkOpen reports whether the namespace's opt-in policy exists.
func (c *Client) AppPrivateNetworkOpen(ctx context.Context, namespace string) (bool, error) {
	policies := c.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(namespace)
	_, err := policies.Get(ctx, AppPrivateNetworkPolicyName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read app private network policy: %w", err)
	}
	return true, nil
}
