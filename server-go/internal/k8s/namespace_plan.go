package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// NamespacePlanLabel carries a project namespace's plan; the log collector
// copies it onto the project's lines and Loki keeps them for that plan (EXC-659).
const NamespacePlanLabel = "excalibase.io/tier"

var namespacePlans = map[string]bool{"free": true, "standard": true, "enterprise": true}

// ProjectNamespacePlans maps every project namespace to its plan label, empty when unset.
func (c *Client) ProjectNamespacePlans(ctx context.Context) (map[string]string, error) {
	list, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: "excalibase.io/type=project"})
	if err != nil {
		return nil, fmt.Errorf("list project namespaces: %w", err)
	}
	plans := make(map[string]string, len(list.Items))
	for _, ns := range list.Items {
		plans[ns.Name] = ns.Labels[NamespacePlanLabel]
	}
	return plans, nil
}

// SetNamespacePlan patches the plan label and nothing else; the chart's
// admission policy refuses any other change provisioning makes to a namespace.
func (c *Client) SetNamespacePlan(ctx context.Context, namespace, plan string) error {
	if !namespacePlans[plan] {
		return fmt.Errorf("plan label %q is not free, standard or enterprise", plan)
	}
	patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, NamespacePlanLabel, plan))
	if _, err := c.clientset.CoreV1().Namespaces().Patch(ctx, namespace, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("label namespace %s with plan %s: %w", namespace, plan, err)
	}
	return nil
}
