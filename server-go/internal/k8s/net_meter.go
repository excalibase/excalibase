package k8s

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

// meteredAppPods matches the pods the app renderer creates and nothing else:
// no database, MOCO, build job, disk job or function runtime carries both labels.
const meteredAppPods = "excalibase.io/component=" + appComponentLabel + ",app.kubernetes.io/managed-by=" + appManagedByValue

// AppPods lists customer app pods in every namespace that have not finished,
// with the project and app they belong to (EXC-844 metering).
func (c *Client) AppPods(ctx context.Context) ([]netmeter.AppPod, error) {
	list, err := c.clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: meteredAppPods})
	if err != nil {
		return nil, fmt.Errorf("list app pods: %w", err)
	}
	pods := make([]netmeter.AppPod, 0, len(list.Items))
	for i := range list.Items {
		pod := &list.Items[i]
		project, app := pod.Labels["excalibase.io/project"], pod.Labels["excalibase.io/app"]
		if project == "" || app == "" || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		pods = append(pods, netmeter.AppPod{UID: string(pod.UID), ProjectID: project, AppID: app, NodeName: pod.Spec.NodeName})
	}
	return pods, nil
}

// Nodes are the cluster's nodes with their internal IPs, by name; a node
// without an internal IP is skipped.
func (c *Client) Nodes(ctx context.Context) ([]netmeter.Node, error) {
	list, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var nodes []netmeter.Node
	for _, node := range list.Items {
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP {
				nodes = append(nodes, netmeter.Node{Name: node.Name, Address: address.Address})
				break
			}
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, nil
}
