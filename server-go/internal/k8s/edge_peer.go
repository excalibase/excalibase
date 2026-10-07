package k8s

import (
	"maps"
	"strconv"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EdgePeer names the public edge's pods. A public platform host resolves to a
// node, whose hostPort is translated to an edge pod before any policy sees the
// packet, so a workload reaches the API or an app only through an allow for
// these pods; the node and every other pod stay closed (EXC-558).
type EdgePeer struct {
	Namespace string
	Labels    map[string]string
	// Ports are the pods' own listeners (8080/8443 behind hostPort 80/443).
	Ports []int
}

func (e EdgePeer) configured() bool {
	return e.Namespace != "" && len(e.Labels) > 0 && len(e.Ports) > 0
}

func (e EdgePeer) ciliumEgressRule() ciliumEgressRule {
	selector := map[string]string{podNamespaceKey: e.Namespace}
	for key, value := range e.Labels {
		selector["k8s:"+key] = value
	}
	ports := make([]ciliumPort, 0, len(e.Ports))
	for _, port := range e.Ports {
		ports = append(ports, ciliumPort{Port: strconv.Itoa(port), Protocol: protocolTCP})
	}
	return ciliumEgressRule{
		ToEndpoints: []metav1.LabelSelector{{MatchLabels: selector}},
		ToPorts:     []ciliumPortRule{{Ports: ports}},
	}
}

func (e EdgePeer) networkPolicyEgressRule() networkingv1.NetworkPolicyEgressRule {
	ports := make([]networkingv1.NetworkPolicyPort, 0, len(e.Ports))
	for _, port := range e.Ports {
		ports = append(ports, tcpPort(port))
	}
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": e.Namespace}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: maps.Clone(e.Labels)},
		}},
		Ports: ports,
	}
}
