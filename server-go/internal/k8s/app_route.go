package k8s

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

const (
	appServicePort     = apphost.ServiceHTTPPort
	appServicePortName = "http"
)

// AppRouteOptions are APP_DOMAIN, APP_INGRESS_CLASS, APP_DOMAIN_ISSUER and the ingress controller's identity.
type AppRouteOptions struct {
	Domain       string
	IngressClass string
	// Issuer is the ACME ClusterIssuer each app's own hostname gets a
	// certificate from; empty serves HTTP only.
	Issuer string
	// WildcardTLS: the edge's default certificate covers every app hostname, so
	// an app asks for no certificate of its own and is served over HTTPS at once.
	WildcardTLS          bool
	IngressFromNamespace string
	// IngressFromLabels narrows the controller's namespace to its pods; empty admits the whole namespace.
	IngressFromLabels map[string]string
}

// Public is the route as the API reports it.
func (o AppRouteOptions) Public() apphost.Route {
	return apphost.Route{Domain: o.Domain, TLS: o.Issuer != "" || o.WildcardTLS}
}

func (o AppRouteOptions) validate() error {
	if o.IngressClass == "" {
		return fmt.Errorf("%w: no ingress class", ErrRenderApp)
	}
	if !namespacePattern.MatchString(o.IngressFromNamespace) {
		return fmt.Errorf("%w: ingress controller namespace %q is not a namespace", ErrRenderApp, o.IngressFromNamespace)
	}
	return nil
}

// AppServiceName is the app's own name, so its project's apps reach it as
// http://<name> and <name>:<port> (EXC-524); apphost.ValidateName keeps it off
// every Service name the platform uses in the namespace.
func AppServiceName(appName string) string { return appName }

// AppIngressPolicyName is the name of the fence admitting only the ingress controller.
func AppIngressPolicyName(appName string) string { return appObjectPrefix + appName + "-ingress" }

type appRoute struct {
	service      *corev1.Service
	ingress      *networkingv1.Ingress
	policy       *unstructured.Unstructured
	certificate  *unstructured.Unstructured
	solverPolicy *unstructured.Unstructured
	// dropCertificate: an internal service keeps no certificate it had while public.
	dropCertificate bool
}

func buildAppRoute(namespace string, app *apphost.App, opts AppRouteOptions) (*appRoute, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if app.Internal {
		route, err := buildInternalServiceRoute(namespace, app)
		if err != nil {
			return nil, err
		}
		route.dropCertificate = opts.Issuer != "" || opts.WildcardTLS
		return route, nil
	}
	host, err := opts.Public().Hostname(app.Name, app.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRenderApp, err)
	}
	policy, err := buildAppIngressPolicy(namespace, app, opts)
	if err != nil {
		return nil, err
	}
	route := &appRoute{
		service: buildAppService(namespace, app),
		ingress: buildAppIngress(namespace, app, host, opts),
		policy:  policy,
	}
	if opts.WildcardTLS {
		serveWildcardTLS(route.ingress)
		route.dropCertificate = true
		return route, nil
	}
	if opts.Issuer == "" {
		return route, nil
	}
	route.certificate = buildAppHostCertificate(namespace, app, host, opts.Issuer)
	if route.solverPolicy, err = buildAcmeSolverPolicy(namespace, opts); err != nil {
		return nil, err
	}
	return route, nil
}

// buildInternalServiceRoute gives an internal service (EXC-525) its Service and
// a fence admitting only the node's readiness probe; no Ingress, no edge.
// The project's private network policy is what lets sibling apps in.
func buildInternalServiceRoute(namespace string, app *apphost.App) (*appRoute, error) {
	probe := []ciliumPortRule{{Ports: []ciliumPort{{Port: strconv.Itoa(appProbePort(app)), Protocol: protocolTCP}}}}
	spec := ciliumPolicySpec{
		EndpointSelector: metav1.LabelSelector{MatchLabels: appSelectorLabels(app)},
		Ingress:          []ciliumIngressRule{{FromEntities: []string{hostEntity}, ToPorts: probe}},
	}
	policy, err := newCiliumPolicy(namespace, AppIngressPolicyName(app.Name), app, spec)
	if err != nil {
		return nil, err
	}
	return &appRoute{service: buildAppService(namespace, app), policy: policy}, nil
}

func buildAppService(namespace string, app *apphost.App) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: AppServiceName(app.Name), Namespace: namespace, Labels: appLabels(app)},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: appSelectorLabels(app),
			Ports:    appServicePorts(app),
		},
	}
}

// appServicePorts: port 80 is the HTTP port the edge and http://<name> use;
// each internal port keeps its own number, so <name>:<port> works in the project.
func appServicePorts(app *apphost.App) []corev1.ServicePort {
	ports := []corev1.ServicePort{}
	if !app.Internal {
		ports = append(ports, corev1.ServicePort{
			Name:       appServicePortName,
			Port:       appServicePort,
			TargetPort: intstr.FromInt(app.Port),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	for slot, internal := range app.InternalPorts {
		ports = append(ports, corev1.ServicePort{
			Name:       internalPortName(slot),
			Port:       int32(internal.Port),
			TargetPort: intstr.FromInt(internal.Port),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	return ports
}

// buildAppIngress serves plain HTTP; serveTLS adds HTTPS once a certificate is issued.
func buildAppIngress(namespace string, app *apphost.App, host string, opts AppRouteOptions) *networkingv1.Ingress {
	class := opts.IngressClass
	prefix := networkingv1.PathTypePrefix
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: AppObjectName(app.Name), Namespace: namespace, Labels: appLabels(app)},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &class,
			Rules: []networkingv1.IngressRule{{
				Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Path:     "/",
						PathType: &prefix,
						Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
							Name: AppServiceName(app.Name),
							Port: networkingv1.ServiceBackendPort{Number: appServicePort},
						}},
					}},
				}},
			}},
		},
	}
	return ingress
}

// buildAppIngressPolicy is the only policy that admits anything to an app: the
// project namespace's own allows leave app pods out (ProjectPodsExceptApps),
// so the edge and the node's kubelet probes are all an app accepts.
func buildAppIngressPolicy(namespace string, app *apphost.App, opts AppRouteOptions) (*unstructured.Unstructured, error) {
	from := map[string]string{podNamespaceKey: opts.IngressFromNamespace}
	for key, value := range opts.IngressFromLabels {
		from["k8s:"+key] = value
	}
	appPort := []ciliumPortRule{{Ports: []ciliumPort{{Port: strconv.Itoa(app.Port), Protocol: protocolTCP}}}}
	spec := ciliumPolicySpec{
		EndpointSelector: metav1.LabelSelector{MatchLabels: appSelectorLabels(app)},
		Ingress: []ciliumIngressRule{
			{FromEndpoints: []metav1.LabelSelector{{MatchLabels: from}}, ToPorts: appPort},
			{FromEntities: []string{hostEntity}, ToPorts: appPort},
		},
	}
	return newCiliumPolicy(namespace, AppIngressPolicyName(app.Name), app, spec)
}
