package dockerapps

import "github.com/excalibase/provisioning-poc/internal/k8s"

// singleHostEdge fills the Kubernetes-only route fields the renderer checks;
// the Ingress and policies they shape are never applied on a single host.
const singleHostEdge = "single-host-edge"

// RenderOptions are the render settings on a single host: app hostnames under
// domain, always served over HTTPS by the edge, under the given sandbox
// runtime ("default" when the operator opted out of one).
func RenderOptions(domain, runtime string) k8s.AppRenderOptions {
	return k8s.AppRenderOptions{
		RuntimeClass: runtime,
		Route: k8s.AppRouteOptions{
			Domain: domain, IngressClass: singleHostEdge, IngressFromNamespace: singleHostEdge, WildcardTLS: true,
		},
	}
}
