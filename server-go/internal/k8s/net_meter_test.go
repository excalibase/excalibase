package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func labelledPod(namespace, name, uid string, labels map[string]string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(uid), Labels: labels},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func appLabelSet(project, app string) map[string]string {
	return map[string]string{
		"excalibase.io/component": "app", "excalibase.io/app": app, "excalibase.io/project": project,
		"app.kubernetes.io/managed-by": appManagedByValue,
	}
}

func TestAppPods_ListsCustomerAppPodsOnly(t *testing.T) {
	client := newFakeClient(
		labelledPod("ns1", "app-web-1", "u1", appLabelSet("proj-1", "web"), corev1.PodRunning),
		labelledPod("ns2", "app-api-1", "u2", appLabelSet("proj-2", "api"), corev1.PodPending),
		labelledPod("ns1", "pg-1", "u3", map[string]string{"cnpg.io/podRole": "instance"}, corev1.PodRunning),
		labelledPod("ns1", "build-1", "u4", map[string]string{"excalibase.io/app": "web", "excalibase.io/component": "app-build"}, corev1.PodRunning),
		labelledPod("ns1", "deno", "u5", map[string]string{"app": "deno-runtime"}, corev1.PodRunning),
		labelledPod("ns1", "app-old", "u6", appLabelSet("proj-1", "old"), corev1.PodSucceeded),
		labelledPod("ns1", "no-manager", "u8", map[string]string{"excalibase.io/component": "app", "excalibase.io/app": "web", "excalibase.io/project": "proj-1"}, corev1.PodRunning),
		labelledPod("ns1", "no-project", "u9", map[string]string{"excalibase.io/component": "app", "excalibase.io/app": "web", "app.kubernetes.io/managed-by": appManagedByValue}, corev1.PodRunning),
		labelledPod("ns1", "no-app", "u10", map[string]string{"excalibase.io/component": "app", "excalibase.io/project": "proj-1", "app.kubernetes.io/managed-by": appManagedByValue}, corev1.PodRunning),
		labelledPod("ns1", "unowned", "u7", map[string]string{"excalibase.io/component": "app", "excalibase.io/app": "web"}, corev1.PodRunning),
	)
	pods, err := client.AppPods(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, pod := range pods {
		got[pod.UID] = pod.ProjectID + "/" + pod.AppID
	}
	want := map[string]string{"u1": "proj-1/web", "u2": "proj-2/api"}
	if len(got) != len(want) || got["u1"] != want["u1"] || got["u2"] != want["u2"] {
		t.Fatalf("pods = %v, want %v: only component=app and managed-by, with both ids, not finished", got, want)
	}
}

func TestNodeAddresses_ReturnsInternalIPsOfNodes(t *testing.T) {
	node := func(name string, addresses ...corev1.NodeAddress) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Addresses: addresses}}
	}
	client := newFakeClient(
		node("n1", corev1.NodeAddress{Type: corev1.NodeHostName, Address: "n1"}, corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}),
		node("n2", corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "1.2.3.4"}),
		node("n3", corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.3"}),
	)
	addresses, err := client.NodeAddresses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 2 || addresses[0] != "10.0.0.1" || addresses[1] != "10.0.0.3" {
		t.Fatalf("addresses = %v: internal IPs only, nodes without one skipped", addresses)
	}
}
