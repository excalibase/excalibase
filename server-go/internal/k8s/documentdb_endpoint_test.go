package k8s

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// EXC-409: a DocumentDB project's Mongo endpoint is a second LoadBalancer
// Service on the same shared address, selecting the same pod but forwarding to
// the gateway's port rather than Postgres's.

func serviceFixture(t *testing.T) (*Client, string) {
	t.Helper()
	namespace := "org-a-proj-doc1"
	clientset := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "proj-doc1-postgres-rw", Namespace: namespace},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"cnpg.io/cluster": "proj-doc1-postgres"}},
	})
	return &Client{clientset: clientset}, namespace
}

// The Mongo Service reaches the gateway container, which lives in the very
// same pod as Postgres — so the selector is the primary's, and only the port
// it forwards to differs.
func TestPublicDBServiceCanPublishTheGatewayPort(t *testing.T) {
	client, namespace := serviceFixture(t)
	ctx := context.Background()

	err := client.EnsurePublicDBService(ctx, namespace, PublicDBServiceSpec{
		Name:             "proj-doc1-documentdb-public",
		Port:             31234,
		TargetPort:       config.DocumentDBGatewayPort,
		PortName:         "documentdb",
		ReadWriteService: "proj-doc1-postgres-rw",
		SharedIPKey:      "excalibase-db-edge",
		ProjectID:        "proj-doc1",
	})
	if err != nil {
		t.Fatalf("EnsurePublicDBService: %v", err)
	}

	svc, err := client.clientset.CoreV1().Services(namespace).Get(ctx, "proj-doc1-documentdb-public", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read the service back: %v", err)
	}
	if len(svc.Spec.Ports) != 1 {
		t.Fatalf("ports: %v", svc.Spec.Ports)
	}
	port := svc.Spec.Ports[0]
	if port.Port != 31234 {
		t.Errorf("published port: got %d", port.Port)
	}
	if port.TargetPort.IntValue() != config.DocumentDBGatewayPort {
		t.Errorf("target port: got %v, want the gateway's %d", port.TargetPort, config.DocumentDBGatewayPort)
	}
	if svc.Spec.Selector["cnpg.io/cluster"] != "proj-doc1-postgres" {
		t.Errorf("selector is not the primary's: %v", svc.Spec.Selector)
	}
}

// The Postgres endpoint is unchanged by the new fields: a spec that names no
// target port still reaches 5432, so EXC-410's callers keep working.
func TestPublicDBServiceDefaultsToThePostgresPort(t *testing.T) {
	client, namespace := serviceFixture(t)
	ctx := context.Background()

	err := client.EnsurePublicDBService(ctx, namespace, PublicDBServiceSpec{
		Name:             "proj-doc1-postgres-public",
		Port:             31235,
		ReadWriteService: "proj-doc1-postgres-rw",
		ProjectID:        "proj-doc1",
	})
	if err != nil {
		t.Fatalf("EnsurePublicDBService: %v", err)
	}

	svc, _ := client.clientset.CoreV1().Services(namespace).Get(ctx, "proj-doc1-postgres-public", metav1.GetOptions{})
	if got := svc.Spec.Ports[0].TargetPort.IntValue(); got != postgresPort {
		t.Errorf("target port: got %d, want %d", got, postgresPort)
	}
	if got := svc.Spec.Ports[0].Name; got != "postgres" {
		t.Errorf("port name: got %q", got)
	}
}

// Lifecycle honesty: a Mongo endpoint is only reported working once the
// gateway container is actually ready. The pod can be Running with Postgres
// serving while the gateway is still waiting for it, and a customer told their
// Mongo endpoint is up in that window gets a refused connection.
func TestDocumentDBGatewayReadyReportsTheContainer(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		pod   *corev1.Pod
		ready bool
	}{
		{
			name:  "gateway ready",
			pod:   gatewayPod(true, true),
			ready: true,
		},
		{
			name:  "postgres ready but the gateway is not",
			pod:   gatewayPod(true, false),
			ready: false,
		},
		{
			name:  "pod is not running",
			pod:   pendingGatewayPod(),
			ready: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{clientset: fake.NewSimpleClientset(tc.pod)}
			got, err := client.DocumentDBGatewayReady(ctx, "ns", "proj-doc1-postgres-1")
			if err != nil {
				t.Fatalf("DocumentDBGatewayReady: %v", err)
			}
			if got != tc.ready {
				t.Errorf("ready: got %v, want %v", got, tc.ready)
			}
		})
	}
}

// A pod with no gateway container is not a DocumentDB pod, and saying "ready"
// about a container that is not there would be the same lie in reverse.
func TestDocumentDBGatewayReadyIsFalseWithoutTheContainer(t *testing.T) {
	pod := gatewayPod(true, true)
	pod.Status.ContainerStatuses = pod.Status.ContainerStatuses[:1]
	client := &Client{clientset: fake.NewSimpleClientset(pod)}

	ready, err := client.DocumentDBGatewayReady(context.Background(), "ns", "proj-doc1-postgres-1")
	if err != nil {
		t.Fatalf("DocumentDBGatewayReady: %v", err)
	}
	if ready {
		t.Error("a pod with no gateway container was reported ready")
	}
}

// A pod that is not there at all is not ready, and is not an error either: it
// is what a paused or not-yet-started project looks like.
func TestDocumentDBGatewayReadyIsFalseForAMissingPod(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset()}

	ready, err := client.DocumentDBGatewayReady(context.Background(), "ns", "gone")
	if err != nil {
		t.Fatalf("a missing pod should not be an error: %v", err)
	}
	if ready {
		t.Error("a missing pod was reported ready")
	}
}

func gatewayPod(postgresReady, gatewayReady bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "proj-doc1-postgres-1", Namespace: "ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "postgres", Ready: postgresReady},
				{Name: DocumentDBGatewayContainer, Ready: gatewayReady},
			},
		},
	}
}

func pendingGatewayPod() *corev1.Pod {
	pod := gatewayPod(false, false)
	pod.Status.Phase = corev1.PodPending
	return pod
}
