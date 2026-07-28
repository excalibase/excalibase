package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func node(name string, cpuMilli, memBytes int64, unschedulable bool) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{Unschedulable: unschedulable},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
			},
		},
	}
}

func podWithRequests(name, ns string, phase corev1.PodPhase, cpuMilli, memBytes int64) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status:     corev1.PodStatus{Phase: phase},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "c",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
						corev1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
					},
				},
			}},
		},
	}
}

func TestGetClusterCapacity_SumsNodesAndPods(t *testing.T) {
	c := newFakeClient(
		node("n1", 4000, 8<<30, false),
		node("n2", 2000, 4<<30, false),
		node("cordoned", 8000, 16<<30, true), // skipped
		podWithRequests("running", "default", corev1.PodRunning, 500, 1<<30),
		podWithRequests("pending", "default", corev1.PodPending, 250, 512<<20),
		podWithRequests("done", "default", corev1.PodSucceeded, 1000, 2<<30), // excluded
	)
	cap, err := c.GetClusterCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetClusterCapacity: %v", err)
	}
	// n1 + n2 only (cordoned skipped): 6000m CPU, 12 GiB.
	if cap.AllocatableCPUMilli != 6000 {
		t.Errorf("AllocatableCPUMilli: got %d, want 6000", cap.AllocatableCPUMilli)
	}
	if cap.AllocatableMemBytes != 12<<30 {
		t.Errorf("AllocatableMemBytes: got %d, want %d", cap.AllocatableMemBytes, int64(12<<30))
	}
	// running + pending only (succeeded excluded): 750m, 1.5 GiB.
	if cap.RequestedCPUMilli != 750 {
		t.Errorf("RequestedCPUMilli: got %d, want 750", cap.RequestedCPUMilli)
	}
	if cap.RequestedMemBytes != (1<<30)+(512<<20) {
		t.Errorf("RequestedMemBytes: got %d", cap.RequestedMemBytes)
	}
}

func TestDenoTierResources(t *testing.T) {
	cases := []struct {
		tier       string
		wantCPUReq string
	}{
		{"ENTERPRISE", "100m"},
		{"standard", "50m"},
		{"FREE", "10m"},
		{"", "10m"},
		{"garbage", "10m"},
	}
	for _, c := range cases {
		cpuReq, cpuLim, memReq, memLim := denoTierResources(c.tier)
		if cpuReq != c.wantCPUReq {
			t.Errorf("tier %q: cpuReq got %q, want %q", c.tier, cpuReq, c.wantCPUReq)
		}
		if cpuLim == "" || memReq == "" || memLim == "" {
			t.Errorf("tier %q: empty limit/request %q/%q/%q", c.tier, cpuLim, memReq, memLim)
		}
	}
}

func TestEnsureDenoRuntime_CreatesDeploymentAndService(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	ns := "proj-deno"

	err := c.EnsureDenoRuntime(ctx, ns, DenoRuntimeSpec{
		Image: "custom/deno:1", RuntimeSecret: "shh", Tier: "STANDARD",
	})
	if err != nil {
		t.Fatalf("EnsureDenoRuntime: %v", err)
	}
	// Deployment + Service should now exist.
	if _, err := c.clientset.AppsV1().Deployments(ns).Get(ctx, "deno-runtime", metav1.GetOptions{}); err != nil {
		t.Errorf("deployment not created: %v", err)
	}
	if _, err := c.clientset.CoreV1().Services(ns).Get(ctx, "deno-runtime", metav1.GetOptions{}); err != nil {
		t.Errorf("service not created: %v", err)
	}

	// Second call is idempotent (deployment exists → no error).
	if err := c.EnsureDenoRuntime(ctx, ns, DenoRuntimeSpec{}); err != nil {
		t.Errorf("idempotent EnsureDenoRuntime: %v", err)
	}
}

// EXC-330: the Deno runtime's egress must be fenced to DNS + this project's own
// Postgres, so an isolate escape can't reach Vault, platform-db, cloud metadata,
// the k8s API, or another tenant's namespace.
func TestEnsureDenoRuntime_CreatesEgressPolicy(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	ns := "proj-deno-np"

	if err := c.EnsureDenoRuntime(ctx, ns, DenoRuntimeSpec{Tier: "STANDARD"}); err != nil {
		t.Fatalf("EnsureDenoRuntime: %v", err)
	}

	np, err := c.clientset.NetworkingV1().NetworkPolicies(ns).Get(ctx, "deno-runtime-egress", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("egress policy not created: %v", err)
	}
	if np.Spec.PodSelector.MatchLabels["app"] != "deno-runtime" {
		t.Errorf("policy must target the deno-runtime pod, got %v", np.Spec.PodSelector.MatchLabels)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != "Egress" {
		t.Errorf("expected Egress-only policy (ingress must stay open for /invoke), got %v", np.Spec.PolicyTypes)
	}
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("expected 3 egress allowances (DNS + own-namespace Postgres + provisioning callback), got %d",
			len(np.Spec.Egress))
	}
	// The metadata callback to provisioning must be allowed, or an enforcing CNI
	// silently drops it (the runtime treats that POST as fire-and-forget).
	prov := np.Spec.Egress[2]
	if len(prov.To) != 1 || prov.To[0].NamespaceSelector == nil ||
		prov.To[0].PodSelector.MatchLabels["app"] != "provisioning" {
		t.Errorf("provisioning callback peer must be ns-scoped to the provisioning pod, got %+v", prov.To)
	}
	if len(prov.Ports) != 1 || prov.Ports[0].Port.IntValue() != 24005 {
		t.Errorf("expected only provisioning's API port 24005, got %+v", prov.Ports)
	}
	// The Postgres rule must be namespace-local: a peer with an empty PodSelector
	// and NO NamespaceSelector means "this namespace only".
	pg := np.Spec.Egress[1]
	if len(pg.To) != 1 || pg.To[0].PodSelector == nil || pg.To[0].NamespaceSelector != nil {
		t.Errorf("Postgres egress must be namespace-local, got %+v", pg.To)
	}
	if len(pg.Ports) != 1 || pg.Ports[0].Port.IntValue() != 5432 {
		t.Errorf("expected only port 5432 for the DB rule, got %+v", pg.Ports)
	}
}

func TestGetDeployment(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	exists, err := c.GetDeployment(ctx, "ns", "missing")
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if exists {
		t.Error("expected missing deployment to report false")
	}
	_ = c.EnsureDenoRuntime(ctx, "ns", DenoRuntimeSpec{})
	exists, err = c.GetDeployment(ctx, "ns", "deno-runtime")
	if err != nil || !exists {
		t.Errorf("expected created deployment to exist, got exists=%v err=%v", exists, err)
	}
}

func TestCreateNamespaceWithLabels(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	labels := map[string]string{"excalibase.io/org": "org-1"}
	if err := c.CreateNamespaceWithLabels(ctx, "labeled-ns", labels); err != nil {
		t.Fatalf("CreateNamespaceWithLabels: %v", err)
	}
	ns, err := c.clientset.CoreV1().Namespaces().Get(ctx, "labeled-ns", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace lookup: %v", err)
	}
	if ns.Labels["excalibase.io/org"] != "org-1" {
		t.Errorf("labels not applied: %v", ns.Labels)
	}
}

func TestListNamespaces_FiltersByPrefix(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	for _, n := range []string{"org-a-proj1", "org-a-proj2", "other"} {
		_ = c.CreateNamespace(ctx, n)
	}
	got, err := c.ListNamespaces(ctx, "org-a-")
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("prefix filter: got %d namespaces, want 2 (%v)", len(got), got)
	}

	all, _ := c.ListNamespaces(ctx, "")
	if len(all) < 3 {
		t.Errorf("empty prefix should list all, got %d", len(all))
	}
}
