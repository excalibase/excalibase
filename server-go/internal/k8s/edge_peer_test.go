package k8s

import (
	"maps"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
)

var testEdge = EdgePeer{
	Namespace: "haproxy-controller",
	Labels:    map[string]string{"app.kubernetes.io/name": "kubernetes-ingress"},
	Ports:     []int{8080, 8443},
}

// The edge rule names the controller's pods by namespace and labels, on their own listener ports only.
func assertCiliumEdgeRule(t *testing.T, rule ciliumEgressRule) {
	t.Helper()
	want := map[string]string{
		"k8s:io.kubernetes.pod.namespace": "haproxy-controller",
		"k8s:app.kubernetes.io/name":      "kubernetes-ingress",
	}
	if len(rule.ToEndpoints) != 1 || !maps.Equal(rule.ToEndpoints[0].MatchLabels, want) {
		t.Errorf("the edge rule must select the edge pods only, got %+v", rule.ToEndpoints)
	}
	assertPorts(t, "edge", rule.ToPorts, ciliumPort{Port: "8080", Protocol: "TCP"}, ciliumPort{Port: "8443", Protocol: "TCP"})
	assertNoAddressPeers(t, rule)
}

func TestEdgePeerCiliumRule(t *testing.T) {
	assertCiliumEdgeRule(t, testEdge.ciliumEgressRule())
}

func TestEdgePeerNotConfigured(t *testing.T) {
	for name, edge := range map[string]EdgePeer{
		"empty":     {},
		"no ports":  {Namespace: "haproxy-controller", Labels: testEdge.Labels},
		"no labels": {Namespace: "haproxy-controller", Ports: testEdge.Ports},
	} {
		if edge.configured() {
			t.Errorf("%s: an incomplete edge must render no rule", name)
		}
	}
	if !testEdge.configured() {
		t.Error("a complete edge must render its rule")
	}
}

func assertNetworkPolicyEdgeRule(t *testing.T, rule networkingv1.NetworkPolicyEgressRule) {
	t.Helper()
	if len(rule.To) != 1 {
		t.Fatalf("the edge rule must have one peer, got %+v", rule.To)
	}
	peer := rule.To[0]
	if peer.IPBlock != nil || peer.NamespaceSelector == nil || peer.PodSelector == nil {
		t.Fatalf("the edge peer must be a namespace and pod selector together, got %+v", peer)
	}
	if !maps.Equal(peer.NamespaceSelector.MatchLabels, map[string]string{"kubernetes.io/metadata.name": "haproxy-controller"}) {
		t.Errorf("namespace selector = %v", peer.NamespaceSelector.MatchLabels)
	}
	if !maps.Equal(peer.PodSelector.MatchLabels, testEdge.Labels) {
		t.Errorf("pod selector = %v", peer.PodSelector.MatchLabels)
	}
	got := map[int32]bool{}
	for _, port := range rule.Ports {
		if port.Protocol == nil || *port.Protocol != "TCP" {
			t.Errorf("edge port %+v must be TCP", port)
		}
		got[port.Port.IntVal] = true
	}
	if len(got) != 2 || !got[8080] || !got[8443] {
		t.Errorf("edge ports = %v, want 8080 and 8443", got)
	}
}

func TestEdgePeerNetworkPolicyRule(t *testing.T) {
	assertNetworkPolicyEdgeRule(t, testEdge.networkPolicyEgressRule())
}

// The rule holds its own copy, so a caller changing its labels later cannot widen a rendered policy.
func TestEdgePeerRulesCopyTheLabels(t *testing.T) {
	edge := EdgePeer{Namespace: "edge", Labels: map[string]string{"app": "edge"}, Ports: []int{8443}}
	rule := edge.networkPolicyEgressRule()
	edge.Labels["app"] = "other"
	if rule.To[0].PodSelector.MatchLabels["app"] != "edge" {
		t.Error("the rendered selector shares the caller's map")
	}
}
