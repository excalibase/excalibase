package k8s

import (
	"context"
	"maps"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func denoCiliumSpec(t *testing.T, policy *unstructured.Unstructured) ciliumPolicySpec {
	t.Helper()
	content, _, err := unstructured.NestedMap(policy.Object, "spec")
	if err != nil {
		t.Fatalf("policy spec: %v", err)
	}
	var spec ciliumPolicySpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &spec); err != nil {
		t.Fatalf("decode policy spec: %v", err)
	}
	return spec
}

func mustDenoCiliumPolicy(t *testing.T, allowedHosts []string, edge EdgePeer) ciliumPolicySpec {
	t.Helper()
	policy, err := buildDenoCiliumEgressPolicy(egressNS, allowedHosts, edge)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if policy.GetKind() != ciliumPolicyKind || policy.GetName() != denoEgressPolicyName || policy.GetNamespace() != egressNS {
		t.Fatalf("policy identity = %s %s/%s", policy.GetKind(), policy.GetNamespace(), policy.GetName())
	}
	return denoCiliumSpec(t, policy)
}

// DNS goes through Cilium's DNS proxy, which is what learns the addresses a name may reach.
func assertDNSProxyRule(t *testing.T, rule ciliumEgressRule) {
	t.Helper()
	if len(rule.ToEndpoints) != 1 || !maps.Equal(rule.ToEndpoints[0].MatchLabels,
		map[string]string{"k8s:io.kubernetes.pod.namespace": "kube-system", "k8s:k8s-app": "kube-dns"}) {
		t.Errorf("the DNS rule must select kube-dns in kube-system only, got %+v", rule.ToEndpoints)
	}
	want := []ciliumPortRule{{
		Ports: []ciliumPort{{Port: "53", Protocol: "UDP"}, {Port: "53", Protocol: "TCP"}},
		Rules: &ciliumL7Rules{DNS: []ciliumFQDNSelector{{MatchPattern: "*"}}},
	}}
	if !reflect.DeepEqual(rule.ToPorts, want) {
		t.Errorf("DNS ports = %+v, want %+v", rule.ToPorts, want)
	}
}

func assertProvisioningRule(t *testing.T, rule ciliumEgressRule) {
	t.Helper()
	want := map[string]string{"k8s:io.kubernetes.pod.namespace": platformNamespace(), "k8s:app": "provisioning"}
	if len(rule.ToEndpoints) != 1 || !maps.Equal(rule.ToEndpoints[0].MatchLabels, want) {
		t.Errorf("the provisioning rule must select the provisioning pod only, got %+v", rule.ToEndpoints)
	}
	assertPorts(t, "provisioning", rule.ToPorts, ciliumPort{Port: "24005", Protocol: "TCP"})
}

// The allowlist is admitted by name on its own ports; nothing opens the world entity or a range.
func TestDenoCiliumFenceAdmitsTheAllowlistByName(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "excalibase-platform")
	spec := mustDenoCiliumPolicy(t, []string{"*.github.com:443", "1.1.1.1:443", "api.stripe.com:443", "example.org:8443"}, testEdge)

	if !maps.Equal(spec.EndpointSelector.MatchLabels, map[string]string{"app": denoRuntimeName}) {
		t.Fatalf("the fence must select the runtime pod, got %+v", spec.EndpointSelector)
	}
	if len(spec.Ingress) != 0 {
		t.Fatalf("the fence is egress only, so provisioning still reaches /deploy and /invoke: %+v", spec.Ingress)
	}
	if len(spec.Egress) != 7 {
		t.Fatalf("want DNS, database, provisioning, edge and three allowlist rules, got %d: %+v", len(spec.Egress), spec.Egress)
	}
	assertDNSProxyRule(t, spec.Egress[0])
	assertDatabaseRuleIsLocal(t, spec.Egress[1])
	assertProvisioningRule(t, spec.Egress[2])
	assertCiliumEdgeRule(t, spec.Egress[3])
	wantAllowlist := []ciliumEgressRule{
		{
			ToFQDNs: []ciliumFQDNSelector{{MatchPattern: "**.github.com"}, {MatchName: "api.stripe.com"}},
			ToPorts: []ciliumPortRule{{Ports: []ciliumPort{{Port: "443", Protocol: "TCP"}}}},
		},
		{
			ToCIDRSet: []ciliumCIDRRule{{CIDR: "1.1.1.1/32"}},
			ToPorts:   []ciliumPortRule{{Ports: []ciliumPort{{Port: "443", Protocol: "TCP"}}}},
		},
		{
			ToFQDNs: []ciliumFQDNSelector{{MatchName: "example.org"}},
			ToPorts: []ciliumPortRule{{Ports: []ciliumPort{{Port: "8443", Protocol: "TCP"}}}},
		},
	}
	if !reflect.DeepEqual(spec.Egress[4:], wantAllowlist) {
		t.Errorf("allowlist rules = %+v, want %+v", spec.Egress[4:], wantAllowlist)
	}
	for _, rule := range spec.Egress {
		if len(rule.ToEntities) != 0 {
			t.Errorf("no rule may open an entity, got %+v", rule)
		}
	}
	if len(spec.EgressDeny) != 1 {
		t.Fatalf("want one range deny, got %+v", spec.EgressDeny)
	}
	var denied []string
	for _, cidr := range spec.EgressDeny[0].ToCIDRSet {
		denied = append(denied, cidr.CIDR)
	}
	if !reflect.DeepEqual(denied, wantDeniedRanges) {
		t.Errorf("denied ranges = %v, want %v: a name resolving into them stays closed", denied, wantDeniedRanges)
	}
}

func TestDenoCiliumFenceIPv6Literal(t *testing.T) {
	spec := mustDenoCiliumPolicy(t, []string{"[2606:4700::1111]:443"}, EdgePeer{})
	last := spec.Egress[len(spec.Egress)-1]
	if !reflect.DeepEqual(last.ToCIDRSet, []ciliumCIDRRule{{CIDR: "2606:4700::1111/128"}}) {
		t.Fatalf("an IPv6 literal opens that one address, got %+v", last)
	}
}

// A new project's functions reach no other host: no name, no range, no edge unless one is named.
func TestDenoCiliumFenceWithoutAllowlistOpensNoHost(t *testing.T) {
	spec := mustDenoCiliumPolicy(t, nil, EdgePeer{})
	if len(spec.Egress) != 3 {
		t.Fatalf("want DNS, database and provisioning only, got %+v", spec.Egress)
	}
	for _, rule := range spec.Egress {
		if len(rule.ToFQDNs) != 0 || len(rule.ToCIDRSet) != 0 || len(rule.ToEntities) != 0 {
			t.Errorf("no allowlist must open nothing beyond the platform's own peers, got %+v", rule)
		}
	}
}

func denoNetworkPolicyExists(t *testing.T, c *Client) bool {
	t.Helper()
	_, err := c.clientset.NetworkingV1().NetworkPolicies(egressNS).Get(context.Background(), denoEgressPolicyName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get network policy: %v", err)
	}
	return err == nil
}

func denoCiliumPolicy(t *testing.T, c *Client) ciliumPolicySpec {
	t.Helper()
	policy, err := c.GetCRD(context.Background(), CiliumNetworkPolicyGVR, egressNS, denoEgressPolicyName)
	if err != nil {
		t.Fatalf("the Cilium fence must exist: %v", err)
	}
	return denoCiliumSpec(t, policy)
}

func TestEnsureDenoRuntimeCiliumFQDNCreatesOnlyTheCiliumFence(t *testing.T) {
	c := newFakeClient()
	spec := DenoRuntimeSpec{Image: egressImage, AllowedHosts: []string{"api.stripe.com:443"}, CiliumFQDN: true}
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if denoNetworkPolicyExists(t, c) {
		t.Fatal("a NetworkPolicy beside the Cilium fence would add its own allows to it")
	}
	got := denoCiliumPolicy(t, c)
	if last := got.Egress[len(got.Egress)-1]; !reflect.DeepEqual(last.ToFQDNs, []ciliumFQDNSelector{{MatchName: "api.stripe.com"}}) {
		t.Fatalf("the allowlist must be admitted by name, got %+v", last)
	}
	if v, _ := allowedHostsEnv(denoDeployment(t, c)); v != "api.stripe.com:443" {
		t.Fatalf("the worker's permission is unchanged by the fence kind, got %q", v)
	}
}

// A runtime fenced by the NetworkPolicy before the switch gets the Cilium fence, then loses the NetworkPolicy.
func TestEnsureDenoRuntimeCiliumFQDNReplacesAnExistingNetworkPolicy(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, AllowedHosts: []string{"api.stripe.com:443"}}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if !denoNetworkPolicyExists(t, c) {
		t.Fatal("precondition: the NetworkPolicy fence exists")
	}
	spec.CiliumFQDN = true
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if denoNetworkPolicyExists(t, c) {
		t.Fatal("the NetworkPolicy fence must be removed once the Cilium fence is in place")
	}
	denoCiliumPolicy(t, c)
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatalf("a second ensure with no NetworkPolicy left must succeed: %v", err)
	}
}
