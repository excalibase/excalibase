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
	appServicePort     = 80
	appServicePortName = "http"
)

// AppRouteOptions are APP_DOMAIN, APP_INGRESS_CLASS, APP_TLS_SECRET and the ingress controller's identity.
type AppRouteOptions struct {
	Domain       string
	IngressClass string
	// TLSSecret is optional; empty serves HTTP only.
	TLSSecret            string
	IngressFromNamespace string
	// IngressFromLabels narrows the controller's namespace to its pods; empty admits the whole namespace.
	IngressFromLabels map[string]string
}

// Public is the route as the API reports it.
func (o AppRouteOptions) Public() apphost.Route {
	return apphost.Route{Domain: o.Domain, TLS: o.TLSSecret != ""}
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

// AppIngressPolicyName is the name of the fence admitting only the ingress controller.
func AppIngressPolicyName(appName string) string { return appObjectPrefix + appName + "-ingress" }

type appRoute struct {
	service *corev1.Service
	ingress *networkingv1.Ingress
	policy  *unstructured.Unstructured
}

func buildAppRoute(namespace string, app *apphost.App, opts AppRouteOptions) (*appRoute, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	host, err := opts.Public().Hostname(app.Name, app.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRenderApp, err)
	}
	policy, err := buildAppIngressPolicy(namespace, app, opts)
	if err != nil {
		return nil, err
	}
	return &appRoute{
		service: buildAppService(namespace, app),
		ingress: buildAppIngress(namespace, app, host, opts),
		policy:  policy,
	}, nil
}

func buildAppService(namespace string, app *apphost.App) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: AppObjectName(app.Name), Namespace: namespace, Labels: appLabels(app)},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: appSelectorLabels(app),
			Ports: []corev1.ServicePort{{
				Name:       appServicePortName,
				Port:       appServicePort,
				TargetPort: intstr.FromInt(app.Port),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

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
							Name: AppObjectName(app.Name),
							Port: networkingv1.ServiceBackendPort{Number: appServicePort},
						}},
					}},
				}},
			}},
		},
	}
	if opts.TLSSecret != "" {
		ingress.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{host}, SecretName: opts.TLSSecret}}
	}
	return ingress
}

// buildAppIngressPolicy: once any policy selects a pod for ingress, Cilium denies every
// other source, so other tenants and the app's own namespace are refused by default.
func buildAppIngressPolicy(namespace string, app *apphost.App, opts AppRouteOptions) (*unstructured.Unstructured, error) {
	from := map[string]string{podNamespaceKey: opts.IngressFromNamespace}
	for key, value := range opts.IngressFromLabels {
		from["k8s:"+key] = value
	}
	spec := ciliumPolicySpec{
		EndpointSelector: metav1.LabelSelector{MatchLabels: appSelectorLabels(app)},
		Ingress: []ciliumIngressRule{{
			FromEndpoints: []metav1.LabelSelector{{MatchLabels: from}},
			ToPorts:       []ciliumPortRule{{Ports: []ciliumPort{{Port: strconv.Itoa(app.Port), Protocol: protocolTCP}}}},
		}},
	}
	return newCiliumPolicy(namespace, AppIngressPolicyName(app.Name), app, spec)
}
