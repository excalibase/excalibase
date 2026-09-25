package k8s

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

const gatewayTestNamespace = "org-a-proj-doc1"

func primaryGatewayPod(name, ip string, primary, ready bool) *corev1.Pod {
	role := "replica"
	if primary {
		role = "primary"
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gatewayTestNamespace, Labels: map[string]string{
			"cnpg.io/cluster": "proj-doc1-postgres", "cnpg.io/instanceRole": role,
		}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: ip,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "postgres", Ready: true},
				{Name: DocumentDBGatewayContainer, Ready: ready},
			},
		},
	}
}

func gatewayClient(pods ...*corev1.Pod) *Client {
	objects := []runtime.Object{&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "proj-doc1-postgres-rw", Namespace: gatewayTestNamespace},
		Spec: corev1.ServiceSpec{Selector: map[string]string{
			"cnpg.io/cluster": "proj-doc1-postgres", "cnpg.io/instanceRole": "primary",
		}},
	}}
	for _, pod := range pods {
		objects = append(objects, pod)
	}
	return &Client{clientset: fake.NewSimpleClientset(objects...)}
}

// The gateway is dialled on the primary the read-write Service selects, which
// follows a failover rather than naming one pod.
func TestDocumentDBGatewayAddressIsThePrimarysPodIP(t *testing.T) {
	client := gatewayClient(
		primaryGatewayPod("proj-doc1-postgres-1", "10.1.0.4", false, true),
		primaryGatewayPod("proj-doc1-postgres-2", "10.1.0.7", true, true),
	)

	got, err := client.DocumentDBGatewayAddress(context.Background(), gatewayTestNamespace, "proj-doc1-postgres-rw")
	if err != nil {
		t.Fatalf("DocumentDBGatewayAddress: %v", err)
	}
	if got != "10.1.0.7" {
		t.Errorf("address: got %q, want the primary's", got)
	}
}

func TestDocumentDBGatewayAddressRefusesAGatewayThatIsNotReady(t *testing.T) {
	client := gatewayClient(primaryGatewayPod("proj-doc1-postgres-1", "10.1.0.4", true, false))

	_, err := client.DocumentDBGatewayAddress(context.Background(), gatewayTestNamespace, "proj-doc1-postgres-rw")
	if !errors.Is(err, ErrDocumentDBGatewayNotReady) {
		t.Fatalf("got %v, want ErrDocumentDBGatewayNotReady", err)
	}
}

func TestDocumentDBGatewayAddressRefusesWhenNoPodIsThere(t *testing.T) {
	client := gatewayClient()

	_, err := client.DocumentDBGatewayAddress(context.Background(), gatewayTestNamespace, "proj-doc1-postgres-rw")
	if !errors.Is(err, ErrDocumentDBGatewayNotReady) {
		t.Fatalf("got %v, want ErrDocumentDBGatewayNotReady", err)
	}
}

func TestDocumentDBGatewayAddressFailsWithoutTheReadWriteService(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset()}

	_, err := client.DocumentDBGatewayAddress(context.Background(), gatewayTestNamespace, "proj-doc1-postgres-rw")
	if err == nil || errors.Is(err, ErrDocumentDBGatewayNotReady) {
		t.Fatalf("got %v, want a lookup failure", err)
	}
}

func TestMockDocumentDBGatewayAddress(t *testing.T) {
	mock := NewMockClient()
	mock.GatewayAddresses = map[string]string{gatewayTestNamespace + "/rw": "10.0.0.1"}
	got, err := mock.DocumentDBGatewayAddress(context.Background(), gatewayTestNamespace, "rw")
	if err != nil || got != "10.0.0.1" {
		t.Fatalf("mock: %q %v", got, err)
	}
	if _, err := mock.DocumentDBGatewayAddress(context.Background(), gatewayTestNamespace, "other"); !errors.Is(err, ErrDocumentDBGatewayNotReady) {
		t.Errorf("unknown: %v", err)
	}
}
