package k8s

import (
	"context"
	"fmt"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/domain"
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
	PodLogs         map[string]string   // key: "namespace/pod" → log tail
	PVCs            map[string][]string // namespace → PersistentVolumeClaim names
	// StuckNamespaces model a namespace whose deletion is accepted but never
	// completes — a finalizer or a Terminating pod holds it. DeleteNamespace
	// returns nil for these, yet the namespace and its contents survive.
	StuckNamespaces map[string]bool
	// TerminatingNamespaces are reported by NamespaceDeleting as on their way out.
	TerminatingNamespaces map[string]bool
	PodReady              map[string]bool
	ExecOutput            map[string]string // key: "namespace/pod" → output
	ExecError             map[string]error
	Metrics               map[string][]PodResourceMetrics
	HelmReleases          map[string]map[string]interface{} // key: "namespace/release" → values
	Calls                 []string                          // track method calls
	ExecCommands          []string                          // every argv ExecInPod was called with, joined by " "
	ExecStdin             []string                          // every non-empty stdin payload ExecInPodStdin was given
	HelmError             error                             // if non-nil, InstallHelmChart returns this error
	NamespaceError        error                             // if non-nil, CreateProjectNamespace returns this error
	DeleteNamespaceError  error                             // if non-nil, DeleteNamespace returns this error
	PodReadyError         error                             // if non-nil, IsPodReady returns this error
	CRDError              error                             // if non-nil, ApplyCRD returns this error
	DeleteCRDError        error                             // if non-nil, DeleteCRD returns this error
	UpdateCRDError        error                             // if non-nil, UpdateCRD returns this error
	UninstallHelmError    error                             // if non-nil, UninstallHelmChart returns this error
	NamespaceExistsError  error                             // if non-nil, NamespaceExists returns this error
	GetPodsError          error                             // if non-nil, GetPods returns this error
	ListPVCsError         error                             // if non-nil, ListPVCs returns this error
	// VolumeExpansionError is what ClusterVolumesExpandable returns; nil means expandable.
	VolumeExpansionError error

	// Wildcards — used when tests don't know the generated project ID upfront.
	WildcardPodReady bool // IsPodReady returns true for any pod not in PodReady
	// AutoReconcileClusters makes ApplyCRD stamp the healthy status a CNPG
	// operator would write, for tests that need a Cluster to be observed
	// ready rather than to exercise the wait itself.
	AutoReconcileClusters bool
	WildcardSecret        map[string][]byte // GetSecret returns this if name not in Secrets
	// NoClusterCA stops GetSecret serving MockClusterCA for an unseeded
	// "<cluster>-ca" Secret, as when the operator has not issued one.
	NoClusterCA       bool
	WildcardExecError error // ExecInPod returns this for any pod not in ExecError

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

	// GatewayReady is what DocumentDBGatewayReady reports, keyed
	// "namespace/pod". A missing key means the gateway is not serving,
	// which is what a project that has none looks like (EXC-409).
	GatewayReady      map[string]bool
	GatewayReadyError error

	ForceDeletePodsError error
	// GatewayAddresses is what DocumentDBGatewayAddress reports, keyed
	// "namespace/readWriteService".
	GatewayAddresses map[string]string

	// DocumentDBServices — "namespace/projectID" of each gateway Service.
	DocumentDBServices           map[string]bool
	EnsureDocumentDBServiceError error

	// PublicDBIngress — open ports per "namespace/projectID".
	PublicDBIngress      map[string][]int
	PublicDBIngressError error

	// OmitClusterAppSecret models an operator that writes no owner Secret
	// for a Cluster it bootstraps.
	OmitClusterAppSecret bool

	// CreateSecretError fails every secret write, so a test can assert what
	// a provision does when the cluster refuses one.
	UpdateSecretError error
	// KeepSecretsError fails KeepSecretsPastOwner.
	KeepSecretsError error
	CreateSecretError error

	// Capacity returned by GetClusterCapacity. Tests set this to simulate
	// cluster headroom for capacity-aware provisioning checks.
	Capacity      ClusterCapacity
	CapacityError error

	// NamespaceQuotas records the last quota ensured per namespace.
	NamespaceQuotas   map[string]NamespaceQuota
	NamespaceQuotaErr error

	// AppPrivateNetwork is the opt-in policy's presence per namespace.
	AppPrivateNetwork    map[string]bool
	AppPrivateNetworkErr error

	AppWorkloads map[string]*AppWorkload // keyed "namespace/deploymentName"
	// WithdrawnWorkloads lists the namespaces WithdrawProjectWorkloads ran on.
	WithdrawnWorkloads []string
	WithdrawErr        error
	// RestartedRuntimes lists the namespaces RestartFunctionRuntime ran on.
	RestartedRuntimes []string
	RestartRuntimeErr error
	// RestoredRoutes holds the app each RestoreAppRoute served, keyed "namespace/appID".
	RestoredRoutes      map[string]*apphost.App
	RestoreRouteErr     error
	ApplyAppWorkloadErr error

	AppRolloutFunc  func(ctx context.Context, namespace, name, deployID string, timeout time.Duration) error
	AppRolloutErr   map[string]error // keyed "namespace/name"
	AppAvailable    map[string]int32 // keyed "namespace/name"
	AppAvailableErr error

	// Lifecycle, keyed "namespace/appID".
	AppPaused      map[string]bool
	AppDeleted     map[string]bool
	AppPauseErr    error
	AppResumeErr   error
	AppPodsGoneErr error
	AppDeleteErr   error
	AppPruneErr    error
	// AppDiskGrown records "namespace/appID=size" per GrowAppDisk call that succeeded.
	AppDiskGrown   []string
	AppDiskGrowErr error
	// AppDiskUsages answers AppDiskUsage by "namespace/appID"; a missing entry is ErrAppDiskNotCreated.
	AppDiskUsages   map[string]AppDiskUsage
	AppDiskUsageErr error
	// AppDiskCopies records "namespace/appID->size@generation" per CopyAppDisk that succeeded.
	AppDiskCopies  []string
	AppDiskCopyErr error
	// AppDiskCreateErr fails CreateAppDisk.
	AppDiskCreateErr error
	AppDiskPruned    []string
	AppDiskPruneErr  error
	// AppDiskRepointed records "namespace/appID=claim" per RepointAppDisk.
	AppDiskRepointed []string
	// Storage is what StorageAllocated answers; StorageErr fails it.
	Storage    StorageAllocation
	StorageErr error
	// VolumeGroupBytes answers LVMVolumeGroupBytes; 0 is ErrStorageCapacityUnknown.
	VolumeGroupBytes int64
	// SizedClassErr answers RequireSizedStorageClass.
	SizedClassErr error
	// PullSecretsDeleted records "namespace/registry" per DeleteRegistryPullSecrets call.
	PullSecretsDeleted   []string
	PullSecretsDeleteErr error
	// AppLogLines answers AppLogs, keyed "namespace/appID".
	AppLogLines  map[string][]AppLogLine
	AppLogsErr   error
	AppLogsAsked []AppLogOptions
	// LivePods answers LiveAppPods, keyed "namespace/appID".
	LivePods    map[string]AppPods
	LivePodsErr error
	// Placement answers RuntimeClassPlacement for any class.
	Placement    RuntimePlacement
	PlacementErr error
	// PausedReplicas answers PausedAppReplicas for any app.
	PausedReplicas    int
	PausedReplicasErr error
	// ResumedTier records the plan size each resume ran at, keyed "namespace/appID".
	ResumedTier map[string]domain.TierType

	// DomainHosts is the last SyncAppDomains, keyed "namespace/appID".
	DomainHosts    map[string][]string
	DomainSyncErr  error
	DomainCerts    map[string]CertificateState // keyed host
	DomainCertErr  error
	IssuerReadyErr error
	// HostCerts answers AppHostCertificate, keyed "namespace/appName"; a missing key is ErrNoCertificate.
	HostCerts         map[string]CertificateState
	HostCertErr       error
	HostCertAttachErr error

	RuntimeClasses    map[string]bool
	RuntimeClassError error
}

func NewMockClient() *MockClient {
	return &MockClient{
		Namespaces:      make(map[string]bool),
		NamespaceLabels: make(map[string]map[string]string),
		HelmReleases:    make(map[string]map[string]interface{}),
		CRDs:            make(map[string]*unstructured.Unstructured),
		Secrets:         make(map[string]map[string][]byte),
		Pods:            make(map[string][]corev1.Pod),
		PodLogs:         make(map[string]string),
		PVCs:            make(map[string][]string),
		StuckNamespaces: make(map[string]bool),
		PodReady:        make(map[string]bool),
		ExecOutput:      make(map[string]string),
		ExecError:       make(map[string]error),
		Metrics:         make(map[string][]PodResourceMetrics),
		DenoRuntimes:    make(map[string]bool),
		DenoSpecs:       make(map[string]DenoRuntimeSpec),

		PublicDBServices: make(map[string]PublicDBServiceSpec),
		GatewayReady:     make(map[string]bool),

		DocumentDBServices: make(map[string]bool),
		PublicDBIngress:    make(map[string][]int),

		AppWorkloads:  make(map[string]*AppWorkload),
		AppRolloutErr: make(map[string]error),
		AppAvailable:  make(map[string]int32),
		AppPaused:     make(map[string]bool),
		AppDeleted:    make(map[string]bool),

		RuntimeClasses: make(map[string]bool),
		DomainHosts:    make(map[string][]string),
		DomainCerts:    make(map[string]CertificateState),
		HostCerts:      make(map[string]CertificateState),
	}
}

// DocumentDBGatewayAddress answers from what the test put in GatewayAddresses.
func (m *MockClient) DocumentDBGatewayAddress(ctx context.Context, namespace, readWriteService string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "DocumentDBGatewayAddress:"+namespace+"/"+readWriteService)
	address, ok := m.GatewayAddresses[namespace+"/"+readWriteService]
	if !ok {
		return "", ErrDocumentDBGatewayNotReady
	}
	return address, nil
}

// ForceDeleteClusterPods records the forced removal of a cluster's pods.
func (m *MockClient) ForceDeleteClusterPods(ctx context.Context, namespace, cluster string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "ForceDeleteClusterPods:"+namespace+"/"+cluster)
	return m.ForceDeletePodsError
}

// DocumentDBGatewayReady answers from what the test put in GatewayReady.
func (m *MockClient) DocumentDBGatewayReady(ctx context.Context, namespace, pod string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "DocumentDBGatewayReady:"+namespace+"/"+pod)
	if m.GatewayReadyError != nil {
		return false, m.GatewayReadyError
	}
	return m.GatewayReady[namespace+"/"+pod], nil
}

// EnsureDocumentDBService records the project's gateway Service.
func (m *MockClient) EnsureDocumentDBService(ctx context.Context, namespace, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "EnsureDocumentDBService:"+namespace+"/"+projectID)
	if m.EnsureDocumentDBServiceError != nil {
		return m.EnsureDocumentDBServiceError
	}
	m.DocumentDBServices[namespace+"/"+projectID] = true
	return nil
}

// EnsurePublicDBIngressPolicy records the ports opened to outside traffic.
func (m *MockClient) EnsurePublicDBIngressPolicy(ctx context.Context, namespace, projectID string, ports []int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "EnsurePublicDBIngressPolicy:"+namespace+"/"+projectID)
	if m.PublicDBIngressError != nil {
		return m.PublicDBIngressError
	}
	m.PublicDBIngress[namespace+"/"+projectID] = append([]int(nil), ports...)
	return nil
}

// DeletePublicDBIngressPolicy forgets the opened ports.
func (m *MockClient) DeletePublicDBIngressPolicy(ctx context.Context, namespace, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "DeletePublicDBIngressPolicy:"+namespace+"/"+projectID)
	if m.PublicDBIngressError != nil {
		return m.PublicDBIngressError
	}
	delete(m.PublicDBIngress, namespace+"/"+projectID)
	return nil
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

func (m *MockClient) CreateProjectNamespace(ctx context.Context, name, orgID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "CreateProjectNamespace:"+name)
	if m.NamespaceError != nil {
		return m.NamespaceError
	}
	labels, err := ProjectNamespaceLabels(orgID)
	if err != nil {
		return err
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

func (m *MockClient) NamespaceDeleting(ctx context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "NamespaceDeleting:"+name)
	return m.TerminatingNamespaces[name], nil
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
	if obj.GetKind() == "Cluster" {
		m.writeClusterAppSecret(namespace, obj.GetName())
	}
	if m.AutoReconcileClusters && obj.GetKind() == "Cluster" {
		markClusterHealthy(obj)
	}
	return nil
}

// writeClusterAppSecret writes the owner Secret CNPG creates for every Cluster
// it bootstraps, unless the test models an operator that wrote none.
func (m *MockClient) writeClusterAppSecret(namespace, cluster string) {
	key := namespace + "/" + cluster + "-app"
	if m.OmitClusterAppSecret {
		return
	}
	if _, ok := m.Secrets[key]; ok {
		return
	}
	m.Secrets[key] = map[string][]byte{"username": []byte("app"), "password": []byte(ClusterAppPassword(cluster))}
}

// ClusterAppPassword is the owner password the mock's operator gives a Cluster.
func ClusterAppPassword(cluster string) string { return "operator-generated-" + cluster }

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
	if m.UpdateCRDError != nil {
		return m.UpdateCRDError
	}
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

func (m *MockClient) PodLogTail(ctx context.Context, namespace, pod, container string, lines int64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "PodLogTail:"+namespace+"/"+pod)
	if out, ok := m.PodLogs[namespace+"/"+pod]; ok {
		return out, nil
	}
	return "", fmt.Errorf("no log: %s/%s", namespace, pod)
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
	if isClusterCASecret(name) && !m.NoClusterCA {
		return MockClusterCA(), nil
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
	if m.CreateSecretError != nil {
		return m.CreateSecretError
	}
	m.Secrets[namespace+"/"+name] = data
	return nil
}

func (m *MockClient) DeleteSecret(ctx context.Context, namespace, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "DeleteSecret:"+namespace+"/"+name)
	delete(m.Secrets, namespace+"/"+name)
	return nil
}

// KeepSecretsPastOwner records the Secrets that would survive their owner;
// the mock models no owner references, so it changes nothing else.
func (m *MockClient) KeepSecretsPastOwner(ctx context.Context, namespace string, names []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range names {
		m.Calls = append(m.Calls, "KeepSecretsPastOwner:"+namespace+"/"+name)
	}
	return m.KeepSecretsError
}

func (m *MockClient) UpdateSecret(ctx context.Context, namespace, name string, data map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "UpdateSecret:"+namespace+"/"+name)
	if m.UpdateSecretError != nil {
		return m.UpdateSecretError
	}
	if _, ok := m.Secrets[namespace+"/"+name]; !ok {
		return fmt.Errorf("secret not found: %s/%s", namespace, name)
	}
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
	metricsOutput := "HTTP/1.0 200 OK\r\nContent-Type: text/plain\r\n\r\n" + `# HELP cnpg_backends_total
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
	if obj, ok := m.CRDs[namespace+"/"+gvr.Resource]; ok {
		return []*unstructured.Unstructured{obj}, nil
	}
	var listed []*unstructured.Unstructured
	for key, obj := range m.CRDs {
		if strings.HasPrefix(key, namespace+"/") && strings.ToLower(obj.GetKind())+"s" == gvr.Resource {
			listed = append(listed, obj)
		}
	}
	return listed, nil
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

func (m *MockClient) ApplyAppWorkload(ctx context.Context, namespace string, workload *AppWorkload) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := ""
	if workload != nil && workload.Deployment != nil {
		name = workload.Deployment.Name
	}
	m.Calls = append(m.Calls, "ApplyAppWorkload:"+namespace+"/"+name)
	if m.ApplyAppWorkloadErr != nil {
		return m.ApplyAppWorkloadErr
	}
	m.AppWorkloads[namespace+"/"+name] = workload
	return nil
}

func (m *MockClient) WaitForAppRollout(ctx context.Context, namespace, name, deployID string, timeout time.Duration) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, "WaitForAppRollout:"+namespace+"/"+name)
	fn := m.AppRolloutFunc
	err := m.AppRolloutErr[namespace+"/"+name]
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, namespace, name, deployID, timeout)
	}
	return err
}

func (m *MockClient) AppAvailableReplicas(ctx context.Context, namespace, name string) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "AppAvailableReplicas:"+namespace+"/"+name)
	return m.AppAvailable[namespace+"/"+name], m.AppAvailableErr
}

func (m *MockClient) PauseAppWorkload(ctx context.Context, namespace, appID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "PauseAppWorkload:"+namespace+"/"+appID)
	if m.AppPauseErr != nil {
		return m.AppPauseErr
	}
	m.AppPaused[namespace+"/"+appID] = true
	return nil
}

func (m *MockClient) WithdrawProjectWorkloads(ctx context.Context, namespace string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "WithdrawProjectWorkloads:"+namespace)
	if m.WithdrawErr != nil {
		return m.WithdrawErr
	}
	m.WithdrawnWorkloads = append(m.WithdrawnWorkloads, namespace)
	return nil
}

func (m *MockClient) RestartFunctionRuntime(ctx context.Context, namespace string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "RestartFunctionRuntime:"+namespace)
	if m.RestartRuntimeErr != nil {
		return m.RestartRuntimeErr
	}
	m.RestartedRuntimes = append(m.RestartedRuntimes, namespace)
	return nil
}

func (m *MockClient) RestoreAppRoute(ctx context.Context, namespace string, app *apphost.App, opts AppRouteOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "RestoreAppRoute:"+namespace+"/"+app.ID)
	if m.RestoreRouteErr != nil {
		return m.RestoreRouteErr
	}
	if m.RestoredRoutes == nil {
		m.RestoredRoutes = map[string]*apphost.App{}
	}
	m.RestoredRoutes[namespace+"/"+app.ID] = app
	return nil
}

func (m *MockClient) ResumeAppWorkload(ctx context.Context, namespace, appID, appName string, tier domain.TierType, timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "ResumeAppWorkload:"+namespace+"/"+appID)
	if m.AppResumeErr != nil {
		return m.AppResumeErr
	}
	delete(m.AppPaused, namespace+"/"+appID)
	if m.ResumedTier == nil {
		m.ResumedTier = map[string]domain.TierType{}
	}
	m.ResumedTier[namespace+"/"+appID] = tier
	return nil
}

func (m *MockClient) WaitForAppPodsGone(ctx context.Context, namespace, appID string, timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "WaitForAppPodsGone:"+namespace+"/"+appID)
	return m.AppPodsGoneErr
}

func (m *MockClient) DeleteAppWorkload(ctx context.Context, namespace, appID string, timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "DeleteAppWorkload:"+namespace+"/"+appID)
	if m.AppDeleteErr != nil {
		return m.AppDeleteErr
	}
	m.AppDeleted[namespace+"/"+appID] = true
	return nil
}

func (m *MockClient) PruneAppWorkload(ctx context.Context, namespace, appID, keepName string, timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "PruneAppWorkload:"+namespace+"/"+appID+"!="+keepName)
	return m.AppPruneErr
}

func (m *MockClient) AppDiskUsage(ctx context.Context, namespace, appID string, disk apphost.AppDisk, opts DiskJobOptions) (AppDiskUsage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "AppDiskUsage:"+namespace+"/"+appID)
	if m.AppDiskUsageErr != nil {
		return AppDiskUsage{}, m.AppDiskUsageErr
	}
	usage, ok := m.AppDiskUsages[namespace+"/"+appID]
	if !ok {
		return AppDiskUsage{}, ErrAppDiskNotCreated
	}
	return usage, nil
}

func (m *MockClient) CopyAppDisk(ctx context.Context, namespace string, app *apphost.App, to apphost.AppDisk, storageClass string, opts DiskJobOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record := fmt.Sprintf("%s/%s->%s@%d", namespace, app.ID, to.Size, to.Generation)
	m.Calls = append(m.Calls, "CopyAppDisk:"+record)
	if m.AppDiskCopyErr != nil {
		return m.AppDiskCopyErr
	}
	m.AppDiskCopies = append(m.AppDiskCopies, record)
	return nil
}

func (m *MockClient) DeleteOtherAppDisks(ctx context.Context, namespace, appID string, keep int, timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record := fmt.Sprintf("%s/%s@%d", namespace, appID, keep)
	m.Calls = append(m.Calls, "DeleteOtherAppDisks:"+record)
	if m.AppDiskPruneErr != nil {
		return m.AppDiskPruneErr
	}
	m.AppDiskPruned = append(m.AppDiskPruned, record)
	return nil
}

func (m *MockClient) RepointAppDisk(ctx context.Context, namespace, appID, claim string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "RepointAppDisk:"+namespace+"/"+appID+"="+claim)
	m.AppDiskRepointed = append(m.AppDiskRepointed, namespace+"/"+appID+"="+claim)
	return nil
}

func (m *MockClient) GrowAppDisk(ctx context.Context, namespace, appID string, generation int, size string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "GrowAppDisk:"+namespace+"/"+appID+"="+size)
	if m.AppDiskGrowErr != nil {
		return m.AppDiskGrowErr
	}
	m.AppDiskGrown = append(m.AppDiskGrown, namespace+"/"+appID+"="+size)
	return nil
}

func (m *MockClient) DeleteRegistryPullSecrets(ctx context.Context, namespace, registry string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "DeleteRegistryPullSecrets:"+namespace+"/"+registry)
	if m.PullSecretsDeleteErr != nil {
		return m.PullSecretsDeleteErr
	}
	m.PullSecretsDeleted = append(m.PullSecretsDeleted, namespace+"/"+registry)
	return nil
}

func (m *MockClient) AppLogs(ctx context.Context, namespace, appID string, opts AppLogOptions) (AppLogPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "AppLogs:"+namespace+"/"+appID)
	m.AppLogsAsked = append(m.AppLogsAsked, opts)
	if m.AppLogsErr != nil {
		return AppLogPage{}, m.AppLogsErr
	}
	return AppLogPage{Lines: m.AppLogLines[namespace+"/"+appID]}, nil
}

func (m *MockClient) LiveAppPods(ctx context.Context, namespace, appID string) (AppPods, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "LiveAppPods:"+namespace+"/"+appID)
	return m.LivePods[namespace+"/"+appID], m.LivePodsErr
}

func (m *MockClient) PausedAppReplicas(ctx context.Context, namespace, appID, appName string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "PausedAppReplicas:"+namespace+"/"+appID)
	if m.PausedReplicasErr != nil {
		return 0, m.PausedReplicasErr
	}
	return m.PausedReplicas, nil
}

func (m *MockClient) RuntimeClassPlacement(ctx context.Context, name string) (RuntimePlacement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "RuntimeClassPlacement:"+name)
	return m.Placement, m.PlacementErr
}

func (m *MockClient) SyncAppDomains(ctx context.Context, namespace string, app *apphost.App, hosts []string, opts AppDomainOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "SyncAppDomains:"+namespace+"/"+app.ID)
	if m.DomainSyncErr != nil {
		return m.DomainSyncErr
	}
	m.DomainHosts[namespace+"/"+app.ID] = append([]string(nil), hosts...)
	return nil
}

func (m *MockClient) AppDomainCertificate(ctx context.Context, namespace, appName, host string) (CertificateState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.DomainCerts[host], m.DomainCertErr
}

func (m *MockClient) AttachIssuedAppHostCertificates(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "AttachIssuedAppHostCertificates")
	return m.HostCertAttachErr
}

func (m *MockClient) AppHostCertificate(ctx context.Context, namespace, appName string) (CertificateState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.HostCertErr != nil {
		return CertificateState{}, m.HostCertErr
	}
	state, ok := m.HostCerts[namespace+"/"+appName]
	if !ok {
		return CertificateState{}, ErrNoCertificate
	}
	return state, nil
}

func (m *MockClient) ClusterIssuerReady(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "ClusterIssuerReady:"+name)
	return m.IssuerReadyErr
}

func (m *MockClient) RuntimeClassExists(ctx context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "RuntimeClassExists:"+name)
	if m.RuntimeClassError != nil {
		return false, m.RuntimeClassError
	}
	return m.RuntimeClasses[name], nil
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

func (m *MockClient) ClusterVolumesExpandable(ctx context.Context, namespace, clusterName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "ClusterVolumesExpandable:"+namespace+"/"+clusterName)
	return m.VolumeExpansionError
}

func (m *MockClient) SetAppPrivateNetwork(ctx context.Context, namespace string, open bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, fmt.Sprintf("SetAppPrivateNetwork:%s/%t", namespace, open))
	if m.AppPrivateNetworkErr != nil {
		return m.AppPrivateNetworkErr
	}
	if m.AppPrivateNetwork == nil {
		m.AppPrivateNetwork = map[string]bool{}
	}
	m.AppPrivateNetwork[namespace] = open
	return nil
}

func (m *MockClient) AppPrivateNetworkOpen(ctx context.Context, namespace string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.AppPrivateNetworkErr != nil {
		return false, m.AppPrivateNetworkErr
	}
	return m.AppPrivateNetwork[namespace], nil
}

func (m *MockClient) StorageAllocated(ctx context.Context) (StorageAllocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Storage, m.StorageErr
}

// AddStorage records an allocation the way a created claim or cluster would appear.
func (m *MockClient) AddStorage(tenantBytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Storage.TenantBytes += tenantBytes
}

func (m *MockClient) LVMVolumeGroupBytes(ctx context.Context, namespace, volumeGroup string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.VolumeGroupBytes <= 0 {
		return 0, ErrStorageCapacityUnknown
	}
	return m.VolumeGroupBytes, nil
}

func (m *MockClient) RequireSizedStorageClass(ctx context.Context, name string, provisioners []string) error {
	return m.SizedClassErr
}

func (m *MockClient) CreateAppDisk(ctx context.Context, namespace string, app *apphost.App, storageClass string, opts DiskJobOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "CreateAppDisk:"+namespace+"/"+app.ID)
	return m.AppDiskCreateErr
}

func (m *MockClient) EnsureNamespaceQuota(ctx context.Context, namespace string, quota NamespaceQuota) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.NamespaceQuotaErr != nil {
		return m.NamespaceQuotaErr
	}
	if m.NamespaceQuotas == nil {
		m.NamespaceQuotas = map[string]NamespaceQuota{}
	}
	m.NamespaceQuotas[namespace] = quota
	return nil
}
