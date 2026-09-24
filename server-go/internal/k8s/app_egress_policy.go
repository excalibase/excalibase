package k8s

import (
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// CiliumNetworkPolicyGVR: Cilium's format excludes nodes and the cluster by identity, not by an IP list.
var CiliumNetworkPolicyGVR = schema.GroupVersionResource{
	Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies",
}

const (
	ciliumPolicyKind = "CiliumNetworkPolicy"
	// world excludes host, remote-node and kube-apiserver, which are entities of their own.
	worldEntity     = "world"
	smtpPortNumber  = 25
	protocolTCP     = "TCP"
	protocolUDP     = "UDP"
	podNamespaceKey = "k8s:io.kubernetes.pod.namespace"
)

// appDeniedRanges are off-cluster addresses world still covers; link-local holds cloud metadata.
var appDeniedRanges = []string{
	"169.254.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
	"127.0.0.0/8", "0.0.0.0/8", "224.0.0.0/4", "240.0.0.0/4",
}

type ciliumPolicySpec struct {
	EndpointSelector metav1.LabelSelector `json:"endpointSelector"`
	Egress           []ciliumEgressRule   `json:"egress,omitempty"`
	EgressDeny       []ciliumEgressRule   `json:"egressDeny,omitempty"`
}

type ciliumEgressRule struct {
	ToEndpoints []metav1.LabelSelector `json:"toEndpoints,omitempty"`
	ToEntities  []string               `json:"toEntities,omitempty"`
	ToCIDRSet   []ciliumCIDRRule       `json:"toCIDRSet,omitempty"`
	ToPorts     []ciliumPortRule       `json:"toPorts,omitempty"`
}

type ciliumCIDRRule struct {
	CIDR string `json:"cidr"`
}

type ciliumPortRule struct {
	Ports []ciliumPort `json:"ports"`
}

type ciliumPort struct {
	Port     string `json:"port"`
	Protocol string `json:"protocol"`
}

// Cilium deny rules win over every allow, so the denied ranges hold even inside world.
func buildAppEgressPolicy(namespace string, app *apphost.App, extraDenyCIDRs []string) (*unstructured.Unstructured, error) {
	allow := []ciliumEgressRule{appDNSRule()}
	if referencesDatabase(app) {
		allow = append(allow, appOwnDatabaseRule())
	}
	allow = append(allow, ciliumEgressRule{ToEntities: []string{worldEntity}})
	spec := ciliumPolicySpec{
		EndpointSelector: metav1.LabelSelector{MatchLabels: appSelectorLabels(app)},
		Egress:           allow,
		EgressDeny:       []ciliumEgressRule{appDeniedRangesRule(extraDenyCIDRs), appSMTPDenyRule()},
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
	if err != nil {
		return nil, fmt.Errorf("%w: egress policy: %w", ErrRenderApp, err)
	}
	policy := &unstructured.Unstructured{Object: map[string]interface{}{"spec": content}}
	policy.SetAPIVersion(CiliumNetworkPolicyGVR.GroupVersion().String())
	policy.SetKind(ciliumPolicyKind)
	policy.SetName(AppEgressPolicyName(app.Name))
	policy.SetNamespace(namespace)
	policy.SetLabels(appLabels(app))
	return policy, nil
}

// appDNSRule allows kube-dns/CoreDNS only, on port 53.
func appDNSRule() ciliumEgressRule {
	return ciliumEgressRule{
		ToEndpoints: []metav1.LabelSelector{{MatchLabels: map[string]string{
			podNamespaceKey: "kube-system",
			"k8s:k8s-app":   "kube-dns",
		}}},
		ToPorts: []ciliumPortRule{{Ports: []ciliumPort{
			{Port: strconv.Itoa(dnsPortNumber), Protocol: protocolUDP},
			{Port: strconv.Itoa(dnsPortNumber), Protocol: protocolTCP},
		}}},
	}
}

// appOwnDatabaseRule: an empty selector in a namespaced policy is this namespace only.
func appOwnDatabaseRule() ciliumEgressRule {
	return ciliumEgressRule{
		ToEndpoints: []metav1.LabelSelector{{}},
		ToPorts:     []ciliumPortRule{{Ports: []ciliumPort{{Port: strconv.Itoa(postgresPortNumber), Protocol: protocolTCP}}}},
	}
}

func appDeniedRangesRule(extraDenyCIDRs []string) ciliumEgressRule {
	cidrs := make([]ciliumCIDRRule, 0, len(appDeniedRanges)+len(extraDenyCIDRs))
	for _, cidr := range append(append([]string{}, appDeniedRanges...), extraDenyCIDRs...) {
		cidrs = append(cidrs, ciliumCIDRRule{CIDR: cidr})
	}
	return ciliumEgressRule{ToCIDRSet: cidrs}
}

// appSMTPDenyRule keeps one tenant from getting the platform's IPs blacklisted for spam.
func appSMTPDenyRule() ciliumEgressRule {
	return ciliumEgressRule{
		ToEntities: []string{worldEntity},
		ToPorts:    []ciliumPortRule{{Ports: []ciliumPort{{Port: strconv.Itoa(smtpPortNumber), Protocol: protocolTCP}}}},
	}
}
