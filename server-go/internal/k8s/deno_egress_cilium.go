package k8s

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
)

// applyDenoCiliumFence puts the Cilium fence in place, then removes the
// NetworkPolicy one: both select the pod and their allows add up, so the
// NetworkPolicy's open public range would undo the per-name allowlist.
func (c *Client) applyDenoCiliumFence(ctx context.Context, namespace string, spec DenoRuntimeSpec) error {
	policy, err := buildDenoCiliumEgressPolicy(namespace, spec.AllowedHosts, spec.Edge)
	if err != nil {
		return err
	}
	if err := c.applyCiliumPolicy(ctx, namespace, policy, "deno egress policy"); err != nil {
		return err
	}
	err = c.clientset.NetworkingV1().NetworkPolicies(namespace).Delete(ctx, denoEgressPolicyName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove the deno NetworkPolicy fence: %w", err)
	}
	return nil
}

// buildDenoCiliumEgressPolicy fences the runtime pod by host name (EXC-558).
// One runtime serves every function of the project, so the allowlist is the
// project's. DNS goes through Cilium's DNS proxy, and an allowlisted name
// opens only the addresses that proxy has seen it resolve to, on its own port.
// The platform's peers are the same as the NetworkPolicy fence's; the private
// ranges are denied, so a name that resolves into them stays closed.
func buildDenoCiliumEgressPolicy(namespace string, allowedHosts []string, edge EdgePeer) (*unstructured.Unstructured, error) {
	allow := []ciliumEgressRule{denoDNSProxyRule(), appOwnDatabaseRule(), denoCiliumProvisioningRule()}
	if edge.configured() {
		allow = append(allow, edge.ciliumEgressRule())
	}
	allow = append(allow, allowlistRules(edgefn.EgressDestinations(allowedHosts))...)
	spec := ciliumPolicySpec{
		EndpointSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": denoRuntimeName}},
		Egress:           allow,
		EgressDeny:       []ciliumEgressRule{appDeniedRangesRule(nil)},
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
	if err != nil {
		return nil, fmt.Errorf("render deno egress policy: %w", err)
	}
	policy := &unstructured.Unstructured{Object: map[string]interface{}{"spec": content}}
	policy.SetAPIVersion(CiliumNetworkPolicyGVR.GroupVersion().String())
	policy.SetKind(ciliumPolicyKind)
	policy.SetName(denoEgressPolicyName)
	policy.SetNamespace(namespace)
	policy.SetLabels(map[string]string{"excalibase.io/component": "edgefn"})
	return policy, nil
}

func denoDNSProxyRule() ciliumEgressRule {
	rule := appDNSRule()
	rule.ToPorts[0].Rules = &ciliumL7Rules{DNS: []ciliumFQDNSelector{{MatchPattern: "*"}}}
	return rule
}

func denoCiliumProvisioningRule() ciliumEgressRule {
	return ciliumEgressRule{
		ToEndpoints: []metav1.LabelSelector{{MatchLabels: map[string]string{
			podNamespaceKey: platformNamespace(),
			"k8s:app":       "provisioning",
		}}},
		ToPorts: tcpPortRule(provisioningAPIPort),
	}
}

// allowlistRules is, per port in ascending order, one rule for the names and one for the addresses.
func allowlistRules(destinations []edgefn.EgressDestination) []ciliumEgressRule {
	names := map[int][]ciliumFQDNSelector{}
	addresses := map[int][]ciliumCIDRRule{}
	for _, destination := range destinations {
		switch {
		case destination.Addr.IsValid():
			addresses[destination.Port] = append(addresses[destination.Port], ciliumCIDRRule{CIDR: hostPrefix(destination.Addr)})
		case destination.Wildcard:
			// "**." is one or more labels (Cilium 1.19+), as the sandbox reads "*."; "*." would be one label only.
			names[destination.Port] = append(names[destination.Port], ciliumFQDNSelector{MatchPattern: "**." + destination.Host})
		default:
			names[destination.Port] = append(names[destination.Port], ciliumFQDNSelector{MatchName: destination.Host})
		}
	}
	ports := make([]int, 0, len(names)+len(addresses))
	for port := range names {
		ports = append(ports, port)
	}
	for port := range addresses {
		if _, seen := names[port]; !seen {
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	var rules []ciliumEgressRule
	for _, port := range ports {
		if selectors := names[port]; len(selectors) > 0 {
			rules = append(rules, ciliumEgressRule{ToFQDNs: selectors, ToPorts: tcpPortRule(port)})
		}
		if cidrs := addresses[port]; len(cidrs) > 0 {
			rules = append(rules, ciliumEgressRule{ToCIDRSet: cidrs, ToPorts: tcpPortRule(port)})
		}
	}
	return rules
}

func hostPrefix(addr netip.Addr) string {
	return netip.PrefixFrom(addr, addr.BitLen()).String()
}

func tcpPortRule(port int) []ciliumPortRule {
	return []ciliumPortRule{{Ports: []ciliumPort{{Port: strconv.Itoa(port), Protocol: protocolTCP}}}}
}
