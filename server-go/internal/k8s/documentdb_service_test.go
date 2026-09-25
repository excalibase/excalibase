package k8s

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	gatewayServiceNamespace = "org-a-proj-doc1"
	gatewayServiceProject   = "proj-doc1"
)

func primarySelector() map[string]string {
	return map[string]string{"cnpg.io/cluster": "proj-doc1-postgres", "cnpg.io/instanceRole": "primary"}
}

func gatewayServiceClient(objects ...*corev1.Service) *Client {
	clientset := fake.NewSimpleClientset()
	for _, object := range objects {
		_ = clientset.Tracker().Add(object)
	}
	return &Client{clientset: clientset}
}

func readWriteService(selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "proj-doc1-postgres-rw", Namespace: gatewayServiceNamespace},
		Spec:       corev1.ServiceSpec{Selector: selector},
	}
}

func readGatewayService(t *testing.T, client *Client) *corev1.Service {
	t.Helper()
	svc, err := client.clientset.CoreV1().Services(gatewayServiceNamespace).
		Get(context.Background(), DocumentDBServiceName(gatewayServiceProject), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read the gateway service: %v", err)
	}
	return svc
}

func TestDocumentDBServiceExposesTheGatewayPortOnThePrimary(t *testing.T) {
	client := gatewayServiceClient(readWriteService(primarySelector()))

	if err := client.EnsureDocumentDBService(context.Background(), gatewayServiceNamespace, gatewayServiceProject); err != nil {
		t.Fatalf("EnsureDocumentDBService: %v", err)
	}

	svc := readGatewayService(t, client)
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("type: got %q, want ClusterIP", svc.Spec.Type)
	}
	if len(svc.Spec.Ports) != 1 {
		t.Fatalf("ports: %v", svc.Spec.Ports)
	}
	port := svc.Spec.Ports[0]
	if port.Port != config.DocumentDBGatewayPort || port.TargetPort.IntValue() != config.DocumentDBGatewayPort {
		t.Errorf("port: got %d -> %v, want %d -> %d", port.Port, port.TargetPort, config.DocumentDBGatewayPort, config.DocumentDBGatewayPort)
	}
	if port.Protocol != corev1.ProtocolTCP {
		t.Errorf("protocol: got %q", port.Protocol)
	}
	for key, value := range primarySelector() {
		if svc.Spec.Selector[key] != value {
			t.Errorf("selector %s: got %q, want %q", key, svc.Spec.Selector[key], value)
		}
	}
	if len(svc.Spec.Selector) != len(primarySelector()) {
		t.Errorf("selector: got %v", svc.Spec.Selector)
	}
	if _, shared := svc.Annotations[metalLBSharedIPAnnotation]; shared {
		t.Error("an in-cluster service carries the public address annotation")
	}
	if svc.Labels[dbEndpointProjectLabel] != gatewayServiceProject {
		t.Errorf("project label: got %v", svc.Labels)
	}
}

func TestDocumentDBServiceFollowsTheOperatorsSelector(t *testing.T) {
	client := gatewayServiceClient(readWriteService(map[string]string{"cnpg.io/cluster": "proj-doc1-postgres", "role": "primary"}))
	ctx := context.Background()
	if err := client.EnsureDocumentDBService(ctx, gatewayServiceNamespace, gatewayServiceProject); err != nil {
		t.Fatalf("first ensure: %v", err)
	}

	rw, _ := client.clientset.CoreV1().Services(gatewayServiceNamespace).Get(ctx, "proj-doc1-postgres-rw", metav1.GetOptions{})
	rw.Spec.Selector = primarySelector()
	if _, err := client.clientset.CoreV1().Services(gatewayServiceNamespace).Update(ctx, rw, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update rw: %v", err)
	}
	if err := client.EnsureDocumentDBService(ctx, gatewayServiceNamespace, gatewayServiceProject); err != nil {
		t.Fatalf("second ensure: %v", err)
	}

	svc := readGatewayService(t, client)
	if svc.Spec.Selector["cnpg.io/instanceRole"] != "primary" || svc.Spec.Selector["role"] != "" {
		t.Errorf("selector was not re-copied: %v", svc.Spec.Selector)
	}
}

func TestDocumentDBServiceRefusesWithoutTheReadWriteService(t *testing.T) {
	client := gatewayServiceClient()

	if err := client.EnsureDocumentDBService(context.Background(), gatewayServiceNamespace, gatewayServiceProject); err == nil {
		t.Fatal("a gateway service was created with no primary selector to copy")
	}
}

func TestDocumentDBServiceHostIsTheServicesClusterName(t *testing.T) {
	got := DocumentDBServiceHost(gatewayServiceProject, gatewayServiceNamespace)
	want := "proj-doc1-documentdb.org-a-proj-doc1.svc.cluster.local"
	if got != want {
		t.Errorf("host: got %q, want %q", got, want)
	}
}

func TestMockRecordsTheGatewayService(t *testing.T) {
	mock := NewMockClient()
	if err := mock.EnsureDocumentDBService(context.Background(), gatewayServiceNamespace, gatewayServiceProject); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !mock.DocumentDBServices[gatewayServiceNamespace+"/"+gatewayServiceProject] {
		t.Error("the service was not recorded")
	}
	mock.EnsureDocumentDBServiceError = context.Canceled
	if err := mock.EnsureDocumentDBService(context.Background(), gatewayServiceNamespace, gatewayServiceProject); err != context.Canceled {
		t.Errorf("error: got %v", err)
	}
}
