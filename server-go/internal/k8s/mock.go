package k8s

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// MockClient is a test double for KubeClient.
type MockClient struct {
	mu         sync.Mutex
	Namespaces map[string]bool
	CRDs       map[string]*unstructured.Unstructured
	Secrets    map[string]map[string][]byte
	Pods       map[string][]corev1.Pod
	PodReady   map[string]bool
	ExecOutput map[string]string // key: "namespace/pod" → output
	ExecError  map[string]error
	Metrics    map[string][]PodResourceMetrics
	Calls      []string // track method calls
}

func NewMockClient() *MockClient {
	return &MockClient{
		Namespaces: make(map[string]bool),
		CRDs:       make(map[string]*unstructured.Unstructured),
		Secrets:    make(map[string]map[string][]byte),
		Pods:       make(map[string][]corev1.Pod),
		PodReady:   make(map[string]bool),
		ExecOutput: make(map[string]string),
		ExecError:  make(map[string]error),
		Metrics:    make(map[string][]PodResourceMetrics),
	}
}

func (m *MockClient) record(method string) {
	m.mu.Lock()
	m.Calls = append(m.Calls, method)
	m.mu.Unlock()
}

func (m *MockClient) CreateNamespace(ctx context.Context, name string) error {
	m.record("CreateNamespace:" + name)
	m.Namespaces[name] = true
	return nil
}

func (m *MockClient) DeleteNamespace(ctx context.Context, name string) error {
	m.record("DeleteNamespace:" + name)
	delete(m.Namespaces, name)
	return nil
}

func (m *MockClient) ApplyCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error {
	m.record("ApplyCRD:" + namespace + "/" + obj.GetName())
	key := namespace + "/" + obj.GetName()
	m.CRDs[key] = obj
	return nil
}

func (m *MockClient) GetCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	m.record("GetCRD:" + namespace + "/" + name)
	key := namespace + "/" + name
	if obj, ok := m.CRDs[key]; ok {
		return obj, nil
	}
	return nil, fmt.Errorf("not found: %s", key)
}

func (m *MockClient) DeleteCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	m.record("DeleteCRD:" + namespace + "/" + name)
	delete(m.CRDs, namespace+"/"+name)
	return nil
}

func (m *MockClient) GetPods(ctx context.Context, namespace, labelSelector string) ([]corev1.Pod, error) {
	m.record("GetPods:" + namespace)
	return m.Pods[namespace], nil
}

func (m *MockClient) IsPodReady(ctx context.Context, namespace, name string) (bool, error) {
	m.record("IsPodReady:" + namespace + "/" + name)
	key := namespace + "/" + name
	if ready, ok := m.PodReady[key]; ok {
		return ready, nil
	}
	return false, fmt.Errorf("pod not found: %s", key)
}

func (m *MockClient) GetSecret(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	m.record("GetSecret:" + namespace + "/" + name)
	key := namespace + "/" + name
	if data, ok := m.Secrets[key]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("secret not found: %s", key)
}

func (m *MockClient) CreateSecret(ctx context.Context, namespace, name string, data map[string][]byte) error {
	m.record("CreateSecret:" + namespace + "/" + name)
	m.Secrets[namespace+"/"+name] = data
	return nil
}

func (m *MockClient) ExecInPod(ctx context.Context, namespace, pod, container string, cmd []string) (string, error) {
	m.record("ExecInPod:" + namespace + "/" + pod)
	key := namespace + "/" + pod
	if err, ok := m.ExecError[key]; ok && err != nil {
		return "", err
	}
	if out, ok := m.ExecOutput[key]; ok {
		return out, nil
	}
	return "", nil
}

func (m *MockClient) GetPodMetrics(ctx context.Context, namespace string) ([]PodResourceMetrics, error) {
	m.record("GetPodMetrics:" + namespace)
	if metrics, ok := m.Metrics[namespace]; ok {
		return metrics, nil
	}
	return nil, fmt.Errorf("no metrics for %s", namespace)
}

// SetupPostgreSQLMock pre-populates a mock for a standard PostgreSQL provisioning test.
func (m *MockClient) SetupPostgreSQLMock(projectID, namespace string, instances int) {
	// Pods ready
	for i := 1; i <= instances; i++ {
		podName := fmt.Sprintf("%s-postgres-%d", projectID, i)
		m.PodReady[namespace+"/"+podName] = true
		m.Pods[namespace] = append(m.Pods[namespace], corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		})
	}

	// Secret with credentials
	m.Secrets[namespace+"/"+projectID+"-postgres-app"] = map[string][]byte{
		"username": []byte("app"),
		"password": []byte("testpassword123"),
		"dbname":   []byte("app"),
	}

	// CNPG metrics output for port 9187
	metricsOutput := `# HELP cnpg_backends_total
cnpg_backends_total{state="active",datname="app"} 2
cnpg_backends_total{state="idle",datname="app"} 1
cnpg_pg_database_size_bytes{datname="app"} 8388608
cnpg_pg_settings_setting{name="max_connections"} 100
cnpg_collector_last_available_backup_timestamp 1711929600
`
	for i := 1; i <= instances; i++ {
		pod := fmt.Sprintf("%s-postgres-%d", projectID, i)
		m.ExecOutput[namespace+"/"+pod] = metricsOutput
	}

	// Metrics-server data
	var podMetrics []PodResourceMetrics
	for i := 1; i <= instances; i++ {
		podMetrics = append(podMetrics, PodResourceMetrics{
			Name:      fmt.Sprintf("%s-postgres-%d", projectID, i),
			CPUMillis: 20,
			MemoryMB:  100,
		})
	}
	m.Metrics[namespace] = podMetrics
}
