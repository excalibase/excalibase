package k8s

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EXC-325: creating a project namespace must fence it with a default-deny
// ingress policy that admits only same-namespace, the platform namespace, and
// the CNPG/monitoring operators — so one tenant's pods cannot reach another
// tenant's pods on the flat cluster network.
func TestCreateNamespaceWithLabels_AppliesIsolationPolicy(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	ns := "org1-proj-iso"

	if err := c.CreateNamespaceWithLabels(ctx, ns, map[string]string{"x": "y"}); err != nil {
		t.Fatalf("CreateNamespaceWithLabels: %v", err)
	}

	np, err := c.clientset.NetworkingV1().NetworkPolicies(ns).Get(ctx, "namespace-isolation", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("isolation policy not created: %v", err)
	}
	if len(np.Spec.PodSelector.MatchLabels) != 0 {
		t.Errorf("policy must select ALL pods (empty selector), got %v", np.Spec.PodSelector.MatchLabels)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != "Ingress" {
		t.Errorf("expected Ingress-only policy (egress fenced separately), got %v", np.Spec.PolicyTypes)
	}
	if len(np.Spec.Ingress) != 1 {
		t.Fatalf("expected exactly one ingress rule, got %d", len(np.Spec.Ingress))
	}

	from := np.Spec.Ingress[0].From
	sameNS, allowed := false, map[string]bool{}
	for _, peer := range from {
		if peer.PodSelector != nil && peer.NamespaceSelector == nil {
			sameNS = true
			continue
		}
		if peer.NamespaceSelector != nil {
			allowed[peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]] = true
		}
	}
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
