package k8s

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func internalService() *apphost.App {
	app := minimalApp()
	app.Name = "cache"
	app.Internal = true
	app.Port = 0
	app.HealthCheckPath = ""
	app.InternalPorts = []apphost.InternalPort{{Port: 6379, Protocol: apphost.ProtocolTCP}}
	return app
}

// An internal service has no route: no Ingress, and the edge is never admitted (EXC-525).
func TestRenderInternalServiceHasNoRouteAndNoEdge(t *testing.T) {
	workload := mustRender(t, internalService(), newResolver())
	if workload.Ingress != nil {
		t.Fatalf("an internal service must have no Ingress, got %+v", workload.Ingress.Spec)
	}
	spec := privateNetworkSpec(t, workload.IngressPolicy)
	want := []ciliumIngressRule{{
		FromEntities: []string{hostEntity},
		ToPorts:      []ciliumPortRule{{Ports: []ciliumPort{{Port: "6379", Protocol: protocolTCP}}}},
	}}
	if !reflect.DeepEqual(spec.Ingress, want) {
		t.Fatalf("the app's own fence must admit only the node's probe on its first internal port, got %+v", spec.Ingress)
	}
}

func TestRenderInternalServicePortsAndProbe(t *testing.T) {
	workload := mustRender(t, internalService(), newResolver())
	container := workload.Deployment.Spec.Template.Spec.Containers[0]
	wantContainer := []corev1.ContainerPort{{Name: "internal-1", ContainerPort: 6379, Protocol: corev1.ProtocolTCP}}
	if !reflect.DeepEqual(container.Ports, wantContainer) {
		t.Errorf("container ports = %+v", container.Ports)
	}
	if probe := container.ReadinessProbe; probe == nil || probe.TCPSocket == nil || probe.TCPSocket.Port != intstr.FromInt(6379) {
		t.Errorf("readiness must check the first internal port, got %+v", probe)
	}
	wantService := []corev1.ServicePort{{Name: "internal-1", Port: 6379, TargetPort: intstr.FromInt(6379), Protocol: corev1.ProtocolTCP}}
	if !reflect.DeepEqual(workload.Service.Spec.Ports, wantService) {
		t.Errorf("service ports = %+v", workload.Service.Spec.Ports)
	}
}

func TestAppWorkloadGoldenInternalService(t *testing.T) {
	assertGolden(t, "internal-service", renderYAML(t, internalService()))
}

// A public app turned internal loses the route it had.
func TestApplyInternalServiceRemovesTheRouteItHad(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	public := minimalApp()
	public.Name = "cache"
	if err := c.ApplyAppWorkload(ctx, testNamespace, mustRender(t, public, newResolver())); err != nil {
		t.Fatalf("public apply: %v", err)
	}
	if _, err := c.clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName("cache"), metav1.GetOptions{}); err != nil {
		t.Fatalf("the public app must have its route: %v", err)
	}
	if err := c.ApplyAppWorkload(ctx, testNamespace, mustRender(t, internalService(), newResolver())); err != nil {
		t.Fatalf("internal apply: %v", err)
	}
	list, err := c.clientset.NetworkingV1().Ingresses(testNamespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("no route may remain for an internal service: %v %v", ingressNames(list), err)
	}
}

func ingressNames(list *networkingv1.IngressList) []string {
	if list == nil {
		return nil
	}
	names := []string{}
	for _, item := range list.Items {
		names = append(names, item.Name)
	}
	return names
}
