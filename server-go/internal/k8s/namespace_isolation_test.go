package k8s

import (
	"context"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"
)

// EXC-325: creating a project namespace must fence it with a default-deny
// ingress policy that admits only same-namespace, the platform namespace, and
// the CNPG/monitoring operators — so one tenant's pods cannot reach another
// tenant's pods on the flat cluster network.
func TestCreateProjectNamespace_AppliesIsolationPolicy(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	ns := "org1-proj-iso"

	if err := c.CreateProjectNamespace(ctx, ns, "org1"); err != nil {
		t.Fatalf("CreateProjectNamespace: %v", err)
	}

	np, err := c.clientset.NetworkingV1().NetworkPolicies(ns).Get(ctx, "namespace-isolation", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("isolation policy not created: %v", err)
	}
	assertSelectsEveryPodButApps(t, np.Spec.PodSelector)
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != "Ingress" {
		t.Errorf("expected Ingress-only policy (egress fenced separately), got %v", np.Spec.PolicyTypes)
	}
	if len(np.Spec.Ingress) != 1 {
		t.Fatalf("expected exactly one ingress rule, got %d", len(np.Spec.Ingress))
	}

	sameNS, allowed := classifyIngressPeers(np.Spec.Ingress[0].From)
	if !sameNS {
		t.Error("policy must allow same-namespace ingress (postgres replication, watcher→pg)")
	}
	for _, want := range []string{platformNamespace(), "cnpg-system", "monitoring"} {
		if !allowed[want] {
			t.Errorf("policy must allow ingress from %q namespace", want)
		}
	}
	// Crucially, no OTHER tenant namespace is allowed: the only namespace peers
	// are the three operators, plus same-namespace.
	if len(allowed) != 3 {
		t.Errorf("expected exactly 3 namespace peers (platform, cnpg-system, monitoring), got %v", allowed)
	}
}

// Idempotent: re-creating an existing namespace must not error on the policy.
func TestIsolationPolicy_Idempotent(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	ns := "org1-proj-iso2"
	if err := c.ensureNamespaceIsolationPolicy(ctx, ns); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := c.ensureNamespaceIsolationPolicy(ctx, ns); err != nil {
		t.Errorf("second ensure must be a no-op, got %v", err)
	}
}

func classifyIngressPeers(peers []networkingv1.NetworkPolicyPeer) (sameNamespace bool, namespaces map[string]bool) {
	namespaces = map[string]bool{}
	for _, peer := range peers {
		if peer.PodSelector != nil && peer.NamespaceSelector == nil {
			sameNamespace = true
		} else if peer.NamespaceSelector != nil {
			namespaces[peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]] = true
		}
	}
	return sameNamespace, namespaces
}

func projectPodLabelSets() map[string]map[string]string {
	return map[string]map[string]string{
		"database instance": {"cnpg.io/cluster": "proj-postgres", "cnpg.io/podRole": "instance"},
		"edge functions":    {"excalibase.io/component": "edgefn"},
		"unlabelled pod":    {},
	}
}

func appPodLabels() map[string]string {
	return appSelectorLabels(minimalApp())
}

func assertSelectsEveryPodButApps(t *testing.T, selector metav1.LabelSelector) {
	t.Helper()
	compiled, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		t.Fatalf("selector %+v: %v", selector, err)
	}
	for name, podLabels := range projectPodLabelSets() {
		if !compiled.Matches(labels.Set(podLabels)) {
			t.Errorf("the isolation policy must still admit project traffic to the %s", name)
		}
	}
	if compiled.Matches(labels.Set(appPodLabels())) {
		t.Error("the isolation policy must not select app pods: its allows would add to the app's edge-only fence")
	}
}

func TestCreateProjectNamespace_DeniesIngressToEveryPodByDefault(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	ns := "org1-proj-deny"
	if err := c.CreateProjectNamespace(ctx, ns, "org1"); err != nil {
		t.Fatalf("CreateProjectNamespace: %v", err)
	}

	np, err := c.clientset.NetworkingV1().NetworkPolicies(ns).Get(ctx, namespaceDefaultDenyPolicy, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("default-deny policy not created: %v", err)
	}
	if len(np.Spec.PodSelector.MatchLabels) != 0 || len(np.Spec.PodSelector.MatchExpressions) != 0 {
		t.Errorf("default deny must select every pod, apps included, got %+v", np.Spec.PodSelector)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress || len(np.Spec.Ingress) != 0 {
		t.Errorf("default deny must be ingress with no allow rule, got %+v", np.Spec)
	}
}

func TestIsolationPolicy_ConvergesAnOlderShape(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	ns := "org1-proj-old"
	old := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: namespaceIsolationPolicy, Namespace: ns},
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}}},
		},
	}
	if _, err := c.clientset.NetworkingV1().NetworkPolicies(ns).Create(ctx, old, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := c.ensureNamespaceIsolationPolicy(ctx, ns); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	np, err := c.clientset.NetworkingV1().NetworkPolicies(ns).Get(ctx, namespaceIsolationPolicy, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertSelectsEveryPodButApps(t, np.Spec.PodSelector)
	if _, err := c.clientset.NetworkingV1().NetworkPolicies(ns).Get(ctx, namespaceDefaultDenyPolicy, metav1.GetOptions{}); err != nil {
		t.Errorf("an existing namespace must gain the default deny: %v", err)
	}
}

func TestNamespacePoliciesGolden(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "platform")
	var rendered string
	for _, policy := range []*networkingv1.NetworkPolicy{
		buildNamespaceDefaultDenyPolicy("org1-proj"),
		buildNamespaceIsolationPolicy("org1-proj"),
	} {
		encoded, err := yaml.Marshal(policy)
		if err != nil {
			t.Fatalf("encode %s: %v", policy.Name, err)
		}
		rendered += "---\n" + string(encoded)
	}
	assertGoldenAt(t, "testdata/namespace_policies/project.yaml", rendered)
}
