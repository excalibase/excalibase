package k8s

import (
	"context"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

type ciliumIngressPolicySpec struct {
	EndpointSelector metav1.LabelSelector `json:"endpointSelector"`
	Ingress          []ciliumIngressRule  `json:"ingress"`
}

// PublicDBIngressPolicyName is the policy that lets outside traffic through a
// project's namespace isolation while its public endpoint is on.
func PublicDBIngressPolicyName(projectID string) string {
	return projectID + "-db-public-ingress"
}

// PostgresInstanceSelector selects a project's CNPG instance pods.
func PostgresInstanceSelector(projectID string) map[string]string {
	return map[string]string{"cnpg.io/cluster": projectID + postgresSuffix, "cnpg.io/podRole": "instance"}
}

// MySQLInstanceSelector selects a MySQL project's MOCO mysqld pods (ADR 0041).
func MySQLInstanceSelector(projectID string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/created-by": "moco",
		"app.kubernetes.io/name":       "mysql",
		"app.kubernetes.io/instance":   MySQLClusterName(projectID),
	}
}

// EnsurePublicDBIngressPolicy admits traffic from outside the cluster to the
// selected database pods on the given ports, and nothing else.
func (c *Client) EnsurePublicDBIngressPolicy(ctx context.Context, namespace, projectID string, selector map[string]string, ports []int) error {
	policy, err := buildPublicDBIngressPolicy(namespace, projectID, selector, ports)
	if err != nil {
		return err
	}
	return c.applyCiliumPolicy(ctx, namespace, policy, "public database ingress policy")
}

// DeletePublicDBIngressPolicy closes the project's public ingress; an absent
// policy is already closed.
func (c *Client) DeletePublicDBIngressPolicy(ctx context.Context, namespace, projectID string) error {
	err := c.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(namespace).
		Delete(ctx, PublicDBIngressPolicyName(projectID), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete public database ingress policy: %w", err)
	}
	return nil
}

func buildPublicDBIngressPolicy(namespace, projectID string, selector map[string]string, ports []int) (*unstructured.Unstructured, error) {
	rule := ciliumPortRule{Ports: make([]ciliumPort, 0, len(ports))}
	for _, port := range ports {
		rule.Ports = append(rule.Ports, ciliumPort{Port: strconv.Itoa(port), Protocol: protocolTCP})
	}
	spec := ciliumIngressPolicySpec{
		EndpointSelector: metav1.LabelSelector{MatchLabels: selector},
		Ingress:          []ciliumIngressRule{{FromEntities: []string{worldEntity}, ToPorts: []ciliumPortRule{rule}}},
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
	if err != nil {
		return nil, fmt.Errorf("render public database ingress policy: %w", err)
	}
	policy := &unstructured.Unstructured{Object: map[string]interface{}{"spec": content}}
	policy.SetAPIVersion(CiliumNetworkPolicyGVR.GroupVersion().String())
	policy.SetKind(ciliumPolicyKind)
	policy.SetName(PublicDBIngressPolicyName(projectID))
	policy.SetNamespace(namespace)
	policy.SetLabels(map[string]string{
		dbEndpointManagedByLabel: dbEndpointManagedByValue,
		dbEndpointProjectLabel:   projectID,
	})
	return policy, nil
}
