package k8s

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	egressNS    = "egress-ns"
	egressImage = "excalibase/deno-runtime:test"
)

func denoDeployment(t *testing.T, c *Client) *appsv1.Deployment {
	t.Helper()
	dep, err := c.clientset.AppsV1().Deployments(egressNS).Get(context.Background(), denoRuntimeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deno deployment: %v", err)
	}
	return dep
}

func denoEgressPolicy(t *testing.T, c *Client) *networkingv1.NetworkPolicy {
	t.Helper()
	pol, err := c.clientset.NetworkingV1().NetworkPolicies(egressNS).Get(context.Background(), denoEgressPolicyName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deno egress policy: %v", err)
	}
	return pol
}

func allowedHostsEnv(dep *appsv1.Deployment) (string, bool) {
	for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "ALLOWED_HOSTS" {
			return env.Value, true
		}
	}
	return "", false
}

func internetRules(pol *networkingv1.NetworkPolicy) []networkingv1.NetworkPolicyEgressRule {
	var out []networkingv1.NetworkPolicyEgressRule
	for _, rule := range pol.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil {
				out = append(out, rule)
			}
		}
	}
	return out
}

func countActions(c *Client, verb, resource string) int {
	n := 0
	for _, a := range c.clientset.(*fake.Clientset).Actions() {
		if a.GetVerb() == verb && a.GetResource().Resource == resource {
			n++
		}
	}
	return n
}

func TestEnsureDenoRuntime_DefaultIsNoEgress(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	dep := denoDeployment(t, c)
	if v, ok := allowedHostsEnv(dep); !ok || v != "" {
		t.Fatalf("ALLOWED_HOSTS must be present and empty by default, got %q (present=%v)", v, ok)
	}
	if rules := internetRules(denoEgressPolicy(t, c)); len(rules) != 0 {
		t.Fatalf("no allowlist must mean no internet egress rule, got %+v", rules)
	}
}

func TestEnsureDenoRuntime_RendersAllowlistIntoEnvAndPolicy(t *testing.T) {
	c := newFakeClient()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", AllowedHosts: []string{"a.example.com", "b.example.com:8443"}}
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, spec); err != nil {
		t.Fatal(err)
	}
	dep := denoDeployment(t, c)
	if v, _ := allowedHostsEnv(dep); v != "a.example.com:443,b.example.com:8443" {
		t.Fatalf("ALLOWED_HOSTS = %q", v)
	}
	if dep.Spec.Template.Annotations[egressHashAnnotation] == "" {
		t.Fatal("pod template must carry the egress hash annotation so a change rolls the pod")
	}

	rules := internetRules(denoEgressPolicy(t, c))
	if len(rules) != 1 {
		t.Fatalf("want exactly one internet egress rule, got %d", len(rules))
	}
	block := rules[0].To[0].IPBlock
	if block.CIDR != "0.0.0.0/0" {
		t.Fatalf("internet rule cidr = %q", block.CIDR)
	}
	for _, want := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "127.0.0.0/8", "100.64.0.0/10"} {
		if !hasCIDR(block.Except, want) {
			t.Errorf("internet rule must except %s, got %v", want, block.Except)
		}
	}
	ports := map[int32]bool{}
	for _, p := range rules[0].Ports {
		ports[p.Port.IntVal] = true
	}
	if !ports[443] || !ports[8443] || len(ports) != 2 {
		t.Fatalf("internet rule ports = %v, want {443, 8443}", ports)
	}
}

func TestEnsureDenoRuntime_ChangedAllowlistRollsDeploymentAndPolicy(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	first := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", AllowedHosts: []string{"a.example.com"}}
	if err := c.EnsureDenoRuntime(ctx, egressNS, first); err != nil {
		t.Fatal(err)
	}
	hashBefore := denoDeployment(t, c).Spec.Template.Annotations[egressHashAnnotation]

	second := first
	second.AllowedHosts = []string{"b.example.com:9443"}
	if err := c.EnsureDenoRuntime(ctx, egressNS, second); err != nil {
		t.Fatal(err)
	}
	dep := denoDeployment(t, c)
	if v, _ := allowedHostsEnv(dep); v != "b.example.com:9443" {
		t.Fatalf("ALLOWED_HOSTS after change = %q", v)
	}
	if dep.Spec.Template.Annotations[egressHashAnnotation] == hashBefore {
		t.Fatal("egress hash annotation must change with the allowlist")
	}
	if countActions(c, "update", "deployments") != 1 || countActions(c, "update", "networkpolicies") != 1 {
		t.Fatalf("expected one deployment + one policy update, got dep=%d pol=%d",
			countActions(c, "update", "deployments"), countActions(c, "update", "networkpolicies"))
	}
	rules := internetRules(denoEgressPolicy(t, c))
	if len(rules) != 1 || len(rules[0].Ports) != 1 || rules[0].Ports[0].Port.IntVal != 9443 {
		t.Fatalf("policy ports not re-rendered: %+v", rules)
	}
}

func TestEnsureDenoRuntime_UnchangedAllowlistIsANoop(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", AllowedHosts: []string{"a.example.com"}}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if countActions(c, "update", "deployments") != 0 || countActions(c, "update", "networkpolicies") != 0 {
		t.Fatal("same allowlist must not touch the deployment or policy")
	}
	if countActions(c, "create", "deployments") != 1 {
		t.Fatal("deployment must be created exactly once")
	}
}

func TestEnsureDenoRuntime_ClearingAllowlistRemovesInternetRule(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.EnsureDenoRuntime(ctx, egressNS, DenoRuntimeSpec{Image: egressImage, AllowedHosts: []string{"a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDenoRuntime(ctx, egressNS, DenoRuntimeSpec{Image: egressImage}); err != nil {
		t.Fatal(err)
	}
	if v, _ := allowedHostsEnv(denoDeployment(t, c)); v != "" {
		t.Fatalf("ALLOWED_HOSTS should be cleared, got %q", v)
	}
	if rules := internetRules(denoEgressPolicy(t, c)); len(rules) != 0 {
		t.Fatalf("internet rule must be removed when the allowlist is cleared, got %+v", rules)
	}
}

func edgeRules(pol *networkingv1.NetworkPolicy) []networkingv1.NetworkPolicyEgressRule {
	var out []networkingv1.NetworkPolicyEgressRule
	for _, rule := range pol.Spec.Egress {
		for _, peer := range rule.To {
			if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == testEdge.Namespace {
				out = append(out, rule)
			}
		}
	}
	return out
}

// The project's own API host resolves to a node, whose hostPort lands on an edge pod (EXC-558).
func TestEnsureDenoRuntime_ReachesTheEdge(t *testing.T) {
	c := newFakeClient()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", Edge: testEdge}
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, spec); err != nil {
		t.Fatal(err)
	}
	rules := edgeRules(denoEgressPolicy(t, c))
	if len(rules) != 1 {
		t.Fatalf("want exactly one edge rule, got %+v", rules)
	}
	assertNetworkPolicyEdgeRule(t, rules[0])
	if internet := internetRules(denoEgressPolicy(t, c)); len(internet) != 0 {
		t.Fatalf("the edge must not open the internet, got %+v", internet)
	}
}

func TestEnsureDenoRuntime_NoEdgeConfiguredOpensNone(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, DenoRuntimeSpec{Image: egressImage}); err != nil {
		t.Fatal(err)
	}
	if rules := edgeRules(denoEgressPolicy(t, c)); len(rules) != 0 {
		t.Fatalf("no edge configured must render no edge rule, got %+v", rules)
	}
}

// Platform hosts reach the worker's net permission only; the internet rule stays the project's own list.
func TestEnsureDenoRuntime_PlatformHostsAreSandboxOnly(t *testing.T) {
	c := newFakeClient()
	spec := DenoRuntimeSpec{Image: egressImage, PlatformHosts: []string{"api.example.com:443"}, AllowedHosts: []string{"b.example.com"}, Edge: testEdge}
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if v, _ := allowedHostsEnv(denoDeployment(t, c)); v != "api.example.com:443,b.example.com:443" {
		t.Fatalf("ALLOWED_HOSTS = %q", v)
	}
	rules := internetRules(denoEgressPolicy(t, c))
	if len(rules) != 1 || len(rules[0].Ports) != 1 || rules[0].Ports[0].Port.IntVal != 443 {
		t.Fatalf("internet rule must stay the project's list, got %+v", rules)
	}

	alone := newFakeClient()
	if err := alone.EnsureDenoRuntime(context.Background(), egressNS, DenoRuntimeSpec{Image: egressImage, PlatformHosts: []string{"api.example.com:443"}}); err != nil {
		t.Fatal(err)
	}
	if internet := internetRules(denoEgressPolicy(t, alone)); len(internet) != 0 {
		t.Fatalf("a platform host alone must not open the internet, got %+v", internet)
	}
}

// A runtime made before the edge rule existed gets it on its next ensure, with the same allowlist.
func TestEnsureDenoRuntime_ExistingRuntimeGainsTheEdgeRule(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.EnsureDenoRuntime(ctx, egressNS, DenoRuntimeSpec{Image: egressImage}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDenoRuntime(ctx, egressNS, DenoRuntimeSpec{Image: egressImage, Edge: testEdge}); err != nil {
		t.Fatal(err)
	}
	if rules := edgeRules(denoEgressPolicy(t, c)); len(rules) != 1 {
		t.Fatalf("the live policy must gain the edge rule, got %+v", rules)
	}
	if countActions(c, "update", "deployments") != 0 {
		t.Fatal("an unchanged environment must not roll the runtime")
	}
	if err := c.EnsureDenoRuntime(ctx, egressNS, DenoRuntimeSpec{Image: egressImage, Edge: testEdge}); err != nil {
		t.Fatal(err)
	}
	if countActions(c, "update", "networkpolicies") != 1 {
		t.Fatalf("an unchanged policy must not be rewritten, got %d updates", countActions(c, "update", "networkpolicies"))
	}
}

// A policy deleted by hand comes back on the next ensure.
func TestEnsureDenoRuntime_RecreatesAMissingPolicy(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, Edge: testEdge}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if err := c.clientset.NetworkingV1().NetworkPolicies(egressNS).Delete(ctx, denoEgressPolicyName, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if rules := edgeRules(denoEgressPolicy(t, c)); len(rules) != 1 {
		t.Fatalf("the policy must be recreated, got %+v", rules)
	}
}

func hasCIDR(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
