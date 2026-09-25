package k8s

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

const wantHost = "web-abc.apps.example.com"

func tlsRoute() AppRouteOptions {
	route := testRoute
	route.TLSSecret = "apps-wildcard-tls"
	route.IngressFromLabels = map[string]string{"app.kubernetes.io/name": "kubernetes-ingress"}
	return route
}

func renderWithRoute(t *testing.T, route AppRouteOptions) *AppWorkload {
	t.Helper()
	workload, err := RenderAppWorkload(testNamespace, minimalApp(), newResolver(),
		AppRenderOptions{RuntimeClass: testRuntimeClass, Route: route})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return workload
}

func TestRenderAppServiceSelectsTheAppOnPort80(t *testing.T) {
	workload := mustRender(t, minimalApp(), newResolver())
	service := workload.Service
	if service.Name != AppObjectName("web") || service.Namespace != testNamespace {
		t.Errorf("service is %s/%s", service.Namespace, service.Name)
	}
	if service.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("service type = %s, want ClusterIP", service.Spec.Type)
	}
	if !maps.Equal(service.Spec.Selector, workload.Deployment.Spec.Selector.MatchLabels) {
		t.Errorf("service selector %v must be the deployment's %v", service.Spec.Selector, workload.Deployment.Spec.Selector.MatchLabels)
	}
	want := []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080), Protocol: corev1.ProtocolTCP}}
	if !reflect.DeepEqual(service.Spec.Ports, want) {
		t.Errorf("service ports = %+v, want %+v", service.Spec.Ports, want)
	}
	if !maps.Equal(service.Labels, appLabels(minimalApp())) {
		t.Errorf("service labels = %v", service.Labels)
	}
}

func TestRenderAppIngressRoutesTheHostnameToTheService(t *testing.T) {
	ingress := mustRender(t, minimalApp(), newResolver()).Ingress
	if ingress.Name != AppObjectName("web") || ingress.Namespace != testNamespace {
		t.Errorf("ingress is %s/%s", ingress.Namespace, ingress.Name)
	}
	if ingress.Spec.IngressClassName == nil || *ingress.Spec.IngressClassName != "haproxy" {
		t.Errorf("ingress class = %v, want haproxy", ingress.Spec.IngressClassName)
	}
	if len(ingress.Spec.TLS) != 0 {
		t.Errorf("no TLS secret configured, so no TLS section: %+v", ingress.Spec.TLS)
	}
	if len(ingress.Spec.Rules) != 1 || ingress.Spec.Rules[0].Host != wantHost {
		t.Fatalf("ingress rules = %+v, want one rule for %s", ingress.Spec.Rules, wantHost)
	}
	paths := ingress.Spec.Rules[0].HTTP.Paths
	if len(paths) != 1 || paths[0].Path != "/" || *paths[0].PathType != networkingv1.PathTypePrefix {
		t.Fatalf("ingress paths = %+v, want / as a prefix", paths)
	}
	backend := paths[0].Backend.Service
	if backend.Name != AppObjectName("web") || backend.Port.Number != 80 {
		t.Errorf("backend = %+v, want the app service on 80", backend)
	}
}

func TestRenderAppIngressReferencesTheWildcardSecret(t *testing.T) {
	ingress := renderWithRoute(t, tlsRoute()).Ingress
	want := []networkingv1.IngressTLS{{Hosts: []string{wantHost}, SecretName: "apps-wildcard-tls"}}
	if !reflect.DeepEqual(ingress.Spec.TLS, want) {
		t.Errorf("ingress TLS = %+v, want %+v", ingress.Spec.TLS, want)
	}
}

func ingressPolicySpec(t *testing.T, policy *unstructured.Unstructured) ciliumPolicySpec {
	t.Helper()
	var spec ciliumPolicySpec
	content, _, err := unstructured.NestedMap(policy.Object, "spec")
	if err != nil {
		t.Fatalf("policy spec: %v", err)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &spec); err != nil {
		t.Fatalf("decode policy spec: %v", err)
	}
	return spec
}

func TestRenderAppIngressPolicyAdmitsOnlyTheIngressController(t *testing.T) {
	workload := mustRender(t, minimalApp(), newResolver())
	policy := workload.IngressPolicy
	if policy.GetKind() != "CiliumNetworkPolicy" || policy.GetName() != AppIngressPolicyName("web") || policy.GetNamespace() != testNamespace {
		t.Fatalf("ingress policy is %s %s/%s", policy.GetKind(), policy.GetNamespace(), policy.GetName())
	}
	spec := ingressPolicySpec(t, policy)
	if !maps.Equal(spec.EndpointSelector.MatchLabels, workload.Deployment.Spec.Selector.MatchLabels) {
		t.Errorf("endpoint selector %v must select exactly the app's pods", spec.EndpointSelector.MatchLabels)
	}
	if len(spec.Egress) != 0 || len(spec.EgressDeny) != 0 {
		t.Errorf("the ingress policy must leave egress to the egress fence: %+v", spec)
	}
	want := []ciliumIngressRule{{
		FromEndpoints: []metav1.LabelSelector{{MatchLabels: map[string]string{podNamespaceKey: "haproxy-controller"}}},
		ToPorts:       []ciliumPortRule{{Ports: []ciliumPort{{Port: "8080", Protocol: "TCP"}}}},
	}}
	if !reflect.DeepEqual(spec.Ingress, want) {
		t.Errorf("ingress rules = %+v, want %+v", spec.Ingress, want)
	}
}

func TestRenderAppIngressPolicyNarrowsToControllerLabels(t *testing.T) {
	spec := ingressPolicySpec(t, renderWithRoute(t, tlsRoute()).IngressPolicy)
	want := map[string]string{podNamespaceKey: "haproxy-controller", "k8s:app.kubernetes.io/name": "kubernetes-ingress"}
	if len(spec.Ingress) != 1 || len(spec.Ingress[0].FromEndpoints) != 1 || !maps.Equal(spec.Ingress[0].FromEndpoints[0].MatchLabels, want) {
		t.Errorf("ingress peers = %+v, want %v", spec.Ingress, want)
	}
}

func TestRenderAppDeploymentRollsWithoutUnavailability(t *testing.T) {
	deployment := mustRender(t, fullApp(), newResolver()).Deployment
	strategy := deployment.Spec.Strategy
	if strategy.Type != appsv1.RollingUpdateDeploymentStrategyType || strategy.RollingUpdate == nil {
		t.Fatalf("strategy = %+v, want RollingUpdate", strategy)
	}
	if got := strategy.RollingUpdate.MaxUnavailable; got == nil || *got != intstr.FromInt(0) {
		t.Errorf("maxUnavailable = %v, want 0", got)
	}
	if got := strategy.RollingUpdate.MaxSurge; got == nil || *got != intstr.FromInt(1) {
		t.Errorf("maxSurge = %v, want 1", got)
	}
}

func TestRenderAppContainerDrainsBeforeStopping(t *testing.T) {
	container := mustRender(t, minimalApp(), newResolver()).Deployment.Spec.Template.Spec.Containers[0]
	if container.Lifecycle == nil || container.Lifecycle.PreStop == nil || container.Lifecycle.PreStop.Sleep == nil {
		t.Fatalf("the container must sleep before it is stopped, got %+v", container.Lifecycle)
	}
	if container.Lifecycle.PreStop.Sleep.Seconds != appDrainSeconds {
		t.Errorf("preStop sleep = %ds, want %d", container.Lifecycle.PreStop.Sleep.Seconds, appDrainSeconds)
	}
}

func TestRenderAppWorkloadRefusesIncompleteRoute(t *testing.T) {
	cases := map[string]func(*AppRouteOptions){
		"no domain":                  func(r *AppRouteOptions) { r.Domain = "" },
		"no ingress class":           func(r *AppRouteOptions) { r.IngressClass = "" },
		"no ingress namespace":       func(r *AppRouteOptions) { r.IngressFromNamespace = "" },
		"invalid ingress namespace":  func(r *AppRouteOptions) { r.IngressFromNamespace = "Not A Namespace" },
		"domain that is no hostname": func(r *AppRouteOptions) { r.Domain = "apps_example" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			route := testRoute
			mutate(&route)
			_, err := RenderAppWorkload(testNamespace, minimalApp(), newResolver(), AppRenderOptions{RuntimeClass: testRuntimeClass, Route: route})
			if !errors.Is(err, ErrRenderApp) {
				t.Fatalf("want ErrRenderApp, got %v", err)
			}
		})
	}
}

func TestRenderAppWorkloadRefusesAProjectWithNoHostname(t *testing.T) {
	app := minimalApp()
	app.ProjectID = "Proj_ABC"
	_, err := RenderAppWorkload(testNamespace, app, newResolver(), testRenderOptions)
	if !errors.Is(err, ErrRenderApp) || !errors.Is(err, apphost.ErrNoRoute) {
		t.Fatalf("want ErrRenderApp wrapping ErrNoRoute, got %v", err)
	}
}

func TestAppRouteOptionsPublic(t *testing.T) {
	if got := testRoute.Public(); got != (apphost.Route{Domain: "apps.example.com"}) {
		t.Errorf("Public() = %+v", got)
	}
	if !tlsRoute().Public().TLS {
		t.Error("a TLS secret must make the public route https")
	}
}

func TestAppWorkloadGoldenTLSRoute(t *testing.T) {
	assertGolden(t, "tls-route", workloadYAML(t, renderWithRoute(t, tlsRoute())))
}

func TestApplyAppWorkload_CreatesThenUpdatesTheRoute(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.ApplyAppWorkload(ctx, testNamespace, mustRender(t, minimalApp(), newResolver())); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	services := c.clientset.CoreV1().Services(testNamespace)
	created, err := services.Get(ctx, AppObjectName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("service should exist: %v", err)
	}
	// The API server assigns the cluster IP; an update must keep it, since it is immutable.
	created.Spec.ClusterIP = "10.43.0.99"
	created.Spec.ClusterIPs = []string{"10.43.0.99"}
	if _, err := services.Update(ctx, created, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("seed cluster IP: %v", err)
	}

	changed := minimalApp()
	changed.Port = 9090
	updated := renderWithRouteFor(t, changed, tlsRoute())
	if err := c.ApplyAppWorkload(ctx, testNamespace, updated); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	service, err := services.Get(ctx, AppObjectName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("service should still exist: %v", err)
	}
	if service.Spec.ClusterIP != "10.43.0.99" {
		t.Errorf("update lost the cluster IP: %q", service.Spec.ClusterIP)
	}
	if service.Spec.Ports[0].TargetPort != intstr.FromInt(9090) {
		t.Errorf("service target port = %v, want 9090", service.Spec.Ports[0].TargetPort)
	}
	ingress, err := c.clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ingress should exist: %v", err)
	}
	if !reflect.DeepEqual(ingress.Spec, updated.Ingress.Spec) {
		t.Errorf("ingress was not updated: %+v", ingress.Spec)
	}
	policy, err := c.GetCRD(ctx, CiliumNetworkPolicyGVR, testNamespace, AppIngressPolicyName("web"))
	if err != nil {
		t.Fatalf("ingress policy should exist: %v", err)
	}
	if !reflect.DeepEqual(ingressPolicySpec(t, policy), ingressPolicySpec(t, updated.IngressPolicy)) {
		t.Errorf("ingress policy was not updated: %+v", policy.Object["spec"])
	}
}

func renderWithRouteFor(t *testing.T, app *apphost.App, route AppRouteOptions) *AppWorkload {
	t.Helper()
	workload, err := RenderAppWorkload(testNamespace, app, newResolver(), AppRenderOptions{RuntimeClass: testRuntimeClass, Route: route})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return workload
}

func TestApplyAppWorkload_RefusesAWorkloadWithoutItsRoute(t *testing.T) {
	c := newFakeClient()
	for name, strip := range map[string]func(*AppWorkload){
		"service":        func(w *AppWorkload) { w.Service = nil },
		"ingress":        func(w *AppWorkload) { w.Ingress = nil },
		"ingress policy": func(w *AppWorkload) { w.IngressPolicy = nil },
	} {
		t.Run(name, func(t *testing.T) {
			workload := mustRender(t, minimalApp(), newResolver())
			strip(workload)
			if err := c.ApplyAppWorkload(context.Background(), testNamespace, workload); err == nil {
				t.Fatal("a workload missing part of its route must be refused")
			}
		})
	}
}

func TestApplyAppRoute_ErrorsPropagate(t *testing.T) {
	cases := []struct {
		verb, resource, want string
		dynamic, seeded      bool
	}{
		{verb: "get", resource: "services", want: "read app service"},
		{verb: "create", resource: "services", want: "create app service"},
		{verb: "update", resource: "services", want: "update app service", seeded: true},
		{verb: "get", resource: "ingresses", want: "read app ingress"},
		{verb: "create", resource: "ingresses", want: "create app ingress"},
		{verb: "update", resource: "ingresses", want: "update app ingress", seeded: true},
		{verb: "update", resource: "ciliumnetworkpolicies", want: "update app egress policy", dynamic: true, seeded: true},
	}
	for _, tc := range cases {
		t.Run(tc.verb+" "+tc.resource, func(t *testing.T) {
			c := newFakeClient()
			workload := mustRender(t, minimalApp(), newResolver())
			if tc.seeded {
				if err := c.ApplyAppWorkload(context.Background(), testNamespace, workload); err != nil {
					t.Fatalf("seed apply: %v", err)
				}
			}
			prependFailure(c, tc.verb, tc.resource, tc.dynamic)
			err := c.ApplyAppWorkload(context.Background(), testNamespace, workload)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func prependFailure(c *Client, verb, resource string, dynamic bool) {
	if dynamic {
		c.dynamicClient.(*dynamicfake.FakeDynamicClient).PrependReactor(verb, resource, failReactor("boom"))
		return
	}
	c.clientset.(*fake.Clientset).PrependReactor(verb, resource, failReactor("boom"))
}

// The egress fence is applied first, so a failure there must not be masked by the ingress fence.
func TestApplyAppIngressPolicy_ErrorsPropagate(t *testing.T) {
	for verb, want := range map[string]string{"create": "create app ingress policy", "update": "update app ingress policy"} {
		t.Run(verb, func(t *testing.T) {
			c := newFakeClient()
			workload := mustRender(t, minimalApp(), newResolver())
			if verb == "update" {
				if err := c.ApplyAppWorkload(context.Background(), testNamespace, workload); err != nil {
					t.Fatalf("seed apply: %v", err)
				}
			}
			c.dynamicClient.(*dynamicfake.FakeDynamicClient).PrependReactor(verb, "ciliumnetworkpolicies",
				func(action ktesting.Action) (bool, runtime.Object, error) {
					return isIngressPolicyAction(action), nil, errors.New("boom")
				})
			err := c.ApplyAppWorkload(context.Background(), testNamespace, workload)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected %q, got %v", want, err)
			}
		})
	}
}

func isIngressPolicyAction(action ktesting.Action) bool {
	switch typed := action.(type) {
	case ktesting.CreateAction:
		return typed.GetObject().(*unstructured.Unstructured).GetName() == AppIngressPolicyName("web")
	case ktesting.UpdateAction:
		return typed.GetObject().(*unstructured.Unstructured).GetName() == AppIngressPolicyName("web")
	}
	return false
}
