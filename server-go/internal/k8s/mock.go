package k8s

import (
	"context"
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const podNameFmt = "%s-postgres-%d"

// MockClient is a test double for KubeClient.
type MockClient struct {
	mu              sync.Mutex
	Namespaces      map[string]bool
	NamespaceLabels map[string]map[string]string
	CRDs            map[string]*unstructured.Unstructured
	Secrets         map[string]map[string][]byte
	Pods            map[string][]corev1.Pod
	PVCs            map[string][]string // namespace → PersistentVolumeClaim names
	// StuckNamespaces model a namespace whose deletion is accepted but never
	// completes — a finalizer or a Terminating pod holds it. DeleteNamespace
	// returns nil for these, yet the namespace and its contents survive.
	StuckNamespaces      map[string]bool
	PodReady             map[string]bool
	ExecOutput           map[string]string // key: "namespace/pod" → output
	ExecError            map[string]error
	Metrics              map[string][]PodResourceMetrics
	HelmReleases         map[string]map[string]interface{} // key: "namespace/release" → values
	Calls                []string                          // track method calls
	ExecCommands         []string                          // every argv ExecInPod was called with, joined by " "
	ExecStdin            []string                          // every non-empty stdin payload ExecInPodStdin was given
	HelmError            error                             // if non-nil, InstallHelmChart returns this error
	NamespaceError       error                             // if non-nil, CreateNamespace returns this error
	DeleteNamespaceError error                             // if non-nil, DeleteNamespace returns this error
	PodReadyError        error                             // if non-nil, IsPodReady returns this error
	CRDError             error                             // if non-nil, ApplyCRD returns this error
	DeleteCRDError       error                             // if non-nil, DeleteCRD returns this error
	UninstallHelmError   error                             // if non-nil, UninstallHelmChart returns this error
	NamespaceExistsError error                             // if non-nil, NamespaceExists returns this error
	GetPodsError         error                             // if non-nil, GetPods returns this error
	ListPVCsError        error                             // if non-nil, ListPVCs returns this error

	// Wildcards — used when tests don't know the generated project ID upfront.
	WildcardPodReady bool // IsPodReady returns true for any pod not in PodReady
	// AutoReconcileClusters makes ApplyCRD stamp the healthy status a CNPG
	// operator would write, for tests that need a Cluster to be observed
	// ready rather than to exercise the wait itself.
	AutoReconcileClusters bool
	WildcardSecret        map[string][]byte // GetSecret returns this if name not in Secrets
	WildcardExecError     error             // ExecInPod returns this for any pod not in ExecError

	// DenoRuntimes — set of namespaces where EnsureDenoRuntime has been called.
	DenoRuntimes map[string]bool
	// DenoSpecs — the last spec EnsureDenoRuntime received per namespace.
	DenoSpecs       map[string]DenoRuntimeSpec
	EnsureDenoError error

	// PublicDBServices — the spec of each project's public database
	// endpoint Service, keyed "namespace/name". A missing key means no
	// Service exists, which is a project refusing connections (EXC-410).
	PublicDBServices           map[string]PublicDBServiceSpec
	EnsurePublicDBError        error
	DeletePublicDBError        error
	PublicDBServiceExistsError error

	// Capacity returned by GetClusterCapacity. Tests set this to simulate
	// cluster headroom for capacity-aware provisioning checks.
	Capacity      ClusterCapacity
	CapacityError error
}

func NewMockClient() *MockClient {
	return &MockClient{
		Namespaces:      make(map[string]bool),
		NamespaceLabels: make(map[string]map[string]string),
		HelmReleases:    make(map[string]map[string]interface{}),
		CRDs:            make(map[string]*unstructured.Unstructured),
		Secrets:         make(map[string]map[string][]byte),
		Pods:            make(map[string][]corev1.Pod),
		PVCs:            make(map[string][]string),
		StuckNamespaces: make(map[string]bool),
		PodReady:        make(map[string]bool),
		ExecOutput:      make(map[string]string),
		ExecError:       make(map[string]error),
		Metrics:         make(map[string][]PodResourceMetrics),
		DenoRuntimes:    make(map[string]bool),
		DenoSpecs:       make(map[string]DenoRuntimeSpec),

		PublicDBServices: make(map[string]PublicDBServiceSpec),
	}
}

// EnsurePublicDBService records the project's public endpoint Service.
func (m *MockClient) EnsurePublicDBService(ctx context.Context, namespace string, spec PublicDBServiceSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, fmt.Sprintf("EnsurePublicDBService:%s/%s:%d", namespace, spec.Name, spec.Port))
	if m.EnsurePublicDBError != nil {
		return m.EnsurePublicDBError
	}
	m.PublicDBServices[namespace+"/"+spec.Name] = spec
	return nil
}

// PublicDBServiceExists answers truthfully from what Ensure/Delete recorded.
func (m *MockClient) PublicDBServiceExists(ctx context.Context, namespace, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "PublicDBServiceExists:"+namespace+"/"+name)
	if m.PublicDBServiceExistsError != nil {
		return false, m.PublicDBServiceExistsError
	}
	_, ok := m.PublicDBServices[namespace+"/"+name]
	return ok, nil
}

// DeletePublicDBService removes the Service; deleting an absent one succeeds.
func (m *MockClient) DeletePublicDBService(ctx context.Context, namespace, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "DeletePublicDBService:"+namespace+"/"+name)
	if m.DeletePublicDBError != nil {
		return m.DeletePublicDBError
	}
	delete(m.PublicDBServices, namespace+"/"+name)
	return nil
}

func (m *MockClient) CreateNamespace(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "CreateNamespace:"+name)
	if m.NamespaceError != nil {
		return m.NamespaceError
	}
	m.Namespaces[name] = true
	return nil
}

func (m *MockClient) CreateNamespaceWithLabels(ctx context.Context, name string, labels map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "CreateNamespaceWithLabels:"+name)
	if m.NamespaceError != nil {
		return m.NamespaceError
	}
	m.Namespaces[name] = true
	m.NamespaceLabels[name] = labels
	return nil
}

func (m *MockClient) DeleteNamespace(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "DeleteNamespace:"+name)
	if m.DeleteNamespaceError != nil {
		return m.DeleteNamespaceError
	}
	if m.StuckNamespaces[name] {
		return nil
	}
	// Real namespace deletion cascades: nothing inside it survives.
	delete(m.Namespaces, name)
	delete(m.Pods, name)
	delete(m.PVCs, name)
	return nil
}

func (m *MockClient) NamespaceExists(ctx context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "NamespaceExists:"+name)
	if m.NamespaceExistsError != nil {
		return false, m.NamespaceExistsError
	}
	return m.Namespaces[name], nil
}

func (m *MockClient) ListPVCs(ctx context.Context, namespace string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "ListPVCs:"+namespace)
	if m.ListPVCsError != nil {
		return nil, m.ListPVCsError
	}
	return m.PVCs[namespace], nil
}

func (m *MockClient) ApplyCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "ApplyCRD:"+namespace+"/"+obj.GetName())
	if m.CRDError != nil {
		return m.CRDError
	}
	m.CRDs[namespace+"/"+obj.GetName()] = obj
	if m.AutoReconcileClusters && obj.GetKind() == "Cluster" {
		markClusterHealthy(obj)
	}
	return nil
}

// markClusterHealthy writes the status a reconciled CNPG Cluster carries.
// Callers that observe readiness read this; without it an applied Cluster
// looks exactly like one no operator ever picked up.
func markClusterHealthy(obj *unstructured.Unstructured) {
	_ = unstructured.SetNestedMap(obj.Object, map[string]interface{}{
		"phase":          "Cluster in healthy state",
		"currentPrimary": obj.GetName() + "-1",
		"readyInstances": int64(1),
	}, "status")
}

func (m *MockClient) GetCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "GetCRD:"+namespace+"/"+name)
	key := namespace + "/" + name
	if obj, ok := m.CRDs[key]; ok {
		return obj, nil
	}
	return nil, fmt.Errorf("not found: %s", key)
}

func (m *MockClient) UpdateCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := namespace + "/" + obj.GetName()
	m.Calls = append(m.Calls, "UpdateCRD:"+key)
	if _, ok := m.CRDs[key]; !ok {
		return fmt.Errorf("not found: %s", key)
	}
	m.CRDs[key] = obj
	return nil
}

func (m *MockClient) DeleteCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "DeleteCRD:"+namespace+"/"+name)
	if m.DeleteCRDError != nil {
		return m.DeleteCRDError
	}
	delete(m.CRDs, namespace+"/"+name)
	return nil
}

func (m *MockClient) CRDExists(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "CRDExists:"+namespace+"/"+name)
	_, ok := m.CRDs[namespace+"/"+name]
	return ok, nil
}

func (m *MockClient) GetPods(ctx context.Context, namespace, labelSelector string) ([]corev1.Pod, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "GetPods:"+namespace)
	if m.GetPodsError != nil {
		return nil, m.GetPodsError
	}
	return m.Pods[namespace], nil
}

func (m *MockClient) IsPodReady(ctx context.Context, namespace, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "IsPodReady:"+namespace+"/"+name)
	if m.PodReadyError != nil {
		return false, m.PodReadyError
	}
	key := namespace + "/" + name
	if ready, ok := m.PodReady[key]; ok {
		return ready, nil
	}
	if m.WildcardPodReady {
		return true, nil
	}
	return false, fmt.Errorf("pod not found: %s", key)
}

func (m *MockClient) GetSecret(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "GetSecret:"+namespace+"/"+name)
	key := namespace + "/" + name
	if data, ok := m.Secrets[key]; ok {
		return data, nil
	}
	if m.WildcardSecret != nil {
		return m.WildcardSecret, nil
	}
	return nil, fmt.Errorf("secret not found: %s", key)
}

func (m *MockClient) CreateSecret(ctx context.Context, namespace, name string, data map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "CreateSecret:"+namespace+"/"+name)
	m.Secrets[namespace+"/"+name] = data
	return nil
}

func (m *MockClient) ExecInPod(ctx context.Context, namespace, pod, container string, cmd []string) (string, error) {
	return m.exec(namespace, pod, cmd, "")
}

// ExecInPodStdin records the argv and the stdin payload separately, mirroring
// the real client: argv becomes URL query parameters the API server audits,
// stdin stays in the request body.
func (m *MockClient) ExecInPodStdin(ctx context.Context, namespace, pod, container string, cmd []string, stdin string) (string, error) {
	return m.exec(namespace, pod, cmd, stdin)
}

func (m *MockClient) exec(namespace, pod string, cmd []string, stdin string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "ExecInPod:"+namespace+"/"+pod)
	m.ExecCommands = append(m.ExecCommands, strings.Join(cmd, " "))
	if stdin != "" {
		m.ExecStdin = append(m.ExecStdin, stdin)
	}
	key := namespace + "/" + pod
	if err, ok := m.ExecError[key]; ok && err != nil {
		return "", err
	}
	if m.WildcardExecError != nil {
		return "", m.WildcardExecError
	}
	if out, ok := m.ExecOutput[key]; ok {
		return out, nil
	}
	return "", nil
}

func (m *MockClient) GetPodMetrics(ctx context.Context, namespace string) ([]PodResourceMetrics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "GetPodMetrics:"+namespace)
	if metrics, ok := m.Metrics[namespace]; ok {
		return metrics, nil
	}
	return nil, fmt.Errorf("no metrics for %s", namespace)
}

// SetupPostgreSQLMock pre-populates a mock for a standard PostgreSQL provisioning test.
func (m *MockClient) SetupPostgreSQLMock(projectID, namespace string, instances int) {
	// Pods ready
	for i := 1; i <= instances; i++ {
		podName := fmt.Sprintf(podNameFmt, projectID, i)
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
		pod := fmt.Sprintf(podNameFmt, projectID, i)
		m.ExecOutput[namespace+"/"+pod] = metricsOutput
	}

	// Metrics-server data
	var podMetrics []PodResourceMetrics
	for i := 1; i <= instances; i++ {
		podMetrics = append(podMetrics, PodResourceMetrics{
			Name:      fmt.Sprintf(podNameFmt, projectID, i),
			CPUMillis: 20,
			MemoryMB:  100,
		})
	}
	m.Metrics[namespace] = podMetrics
}

func (m *MockClient) ListNamespaces(ctx context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "ListNamespaces:"+prefix)
	var result []string
	for ns := range m.Namespaces {
		if prefix == "" || len(ns) >= len(prefix) && ns[:len(prefix)] == prefix {
			result = append(result, ns)
		}
	}
	return result, nil
}

func (m *MockClient) ListCRDs(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "ListCRDs:"+namespace+"/"+gvr.Resource)
	key := namespace + "/" + gvr.Resource
	if obj, ok := m.CRDs[key]; ok {
		return []*unstructured.Unstructured{obj}, nil
	}
	return nil, nil
}

func (m *MockClient) ApplyManifestURL(ctx context.Context, url string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "ApplyManifestURL:"+url)
	return nil
}

func (m *MockClient) GetDeployment(ctx context.Context, namespace, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "GetDeployment:"+namespace+"/"+name)
	// The per-project Deno runtime is tracked by EnsureDenoRuntime, so its
	// existence is answered truthfully; every other deployment "exists".
	if name == "deno-runtime" {
		return m.DenoRuntimes[namespace], nil
	}
	return true, nil
}

func (m *MockClient) InstallHelmChart(ctx context.Context, namespace, releaseName, chartPath string, values map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "InstallHelmChart:"+namespace+"/"+releaseName)
	if m.HelmError != nil {
		return m.HelmError
	}
	m.HelmReleases[namespace+"/"+releaseName] = values
	return nil
}

func (m *MockClient) EnsureDenoRuntime(ctx context.Context, namespace string, spec DenoRuntimeSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "EnsureDenoRuntime:"+namespace+":"+spec.Tier)
	if m.EnsureDenoError != nil {
		return m.EnsureDenoError
	}
	m.DenoRuntimes[namespace] = true
	m.DenoSpecs[namespace] = spec
	return nil
}

func (m *MockClient) GetClusterCapacity(ctx context.Context) (ClusterCapacity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "GetClusterCapacity")
	if m.CapacityError != nil {
		return ClusterCapacity{}, m.CapacityError
	}
	return m.Capacity, nil
}

func (m *MockClient) UninstallHelmChart(ctx context.Context, namespace, releaseName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "UninstallHelmChart:"+namespace+"/"+releaseName)
	if m.UninstallHelmError != nil {
		return m.UninstallHelmError
	}
	delete(m.HelmReleases, namespace+"/"+releaseName)
	return nil
}
