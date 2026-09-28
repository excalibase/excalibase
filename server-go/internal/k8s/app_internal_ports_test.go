package k8s

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func appWithInternalPorts(ports ...int) *apphost.App {
	app := minimalApp()
	for _, port := range ports {
		app.InternalPorts = append(app.InternalPorts, apphost.InternalPort{Port: port, Protocol: apphost.ProtocolTCP})
	}
	return app
}

// Each internal port is a named container port, so the project's one static
// policy admits it by name (EXC-525).
func TestRenderAppInternalPortsAreNamedContainerPorts(t *testing.T) {
	container := mustRender(t, appWithInternalPorts(6379, 9092), newResolver()).Deployment.Spec.Template.Spec.Containers[0]
	want := []corev1.ContainerPort{
		{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
		{Name: "internal-1", ContainerPort: 6379, Protocol: corev1.ProtocolTCP},
		{Name: "internal-2", ContainerPort: 9092, Protocol: corev1.ProtocolTCP},
	}
	if !reflect.DeepEqual(container.Ports, want) {
		t.Fatalf("container ports = %+v, want %+v", container.Ports, want)
	}
}

func TestRenderAppInternalPortsAreServicePortsUnderTheirOwnNumber(t *testing.T) {
	service := mustRender(t, appWithInternalPorts(6379), newResolver()).Service
	want := []corev1.ServicePort{
		{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080), Protocol: corev1.ProtocolTCP},
		{Name: "internal-1", Port: 6379, TargetPort: intstr.FromInt(6379), Protocol: corev1.ProtocolTCP},
	}
	if !reflect.DeepEqual(service.Spec.Ports, want) {
		t.Fatalf("service ports = %+v, want %+v", service.Spec.Ports, want)
	}
}

// The edge sees only the HTTP port: neither the Ingress nor the app's edge
// fence ever names an internal port.
func TestRenderAppInternalPortsNeverReachTheEdge(t *testing.T) {
	workload := mustRender(t, appWithInternalPorts(6379), newResolver())
	for _, rule := range workload.Ingress.Spec.Rules {
		for _, path := range rule.HTTP.Paths {
			if path.Backend.Service.Port.Number != appServicePort {
				t.Errorf("the ingress routes to port %d", path.Backend.Service.Port.Number)
			}
		}
	}
	spec := privateNetworkSpec(t, workload.IngressPolicy)
	for _, rule := range spec.Ingress {
		for _, ports := range rule.ToPorts {
			for _, port := range ports.Ports {
				if port.Port != "8080" {
					t.Errorf("the edge fence admits port %s", port.Port)
				}
			}
		}
	}
}

func TestAppPrivateNetworkPolicyAdmitsEveryInternalPortName(t *testing.T) {
	policy, err := buildAppPrivateNetworkPolicy(testNamespace)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ports := privateNetworkSpec(t, policy).Ingress[0].ToPorts[0].Ports
	if len(ports) != 1+apphost.MaxInternalPorts || ports[0].Port != "http" || ports[len(ports)-1].Port != "internal-8" {
		t.Fatalf("ports = %+v, want http and internal-1..internal-%d", ports, apphost.MaxInternalPorts)
	}
	for _, port := range ports {
		if port.Protocol != protocolTCP {
			t.Errorf("%s is %s, want TCP", port.Port, port.Protocol)
		}
	}
}

func TestAppWorkloadGoldenInternalPorts(t *testing.T) {
	assertGolden(t, "internal-ports", renderYAML(t, appWithInternalPorts(6379, 9092)))
}
