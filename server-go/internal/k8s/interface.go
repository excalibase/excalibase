package k8s

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// KubeClient abstracts Kubernetes operations for testability.
type KubeClient interface {
	CreateNamespace(ctx context.Context, name string) error
	CreateNamespaceWithLabels(ctx context.Context, name string, labels map[string]string) error
	DeleteNamespace(ctx context.Context, name string) error
	ApplyCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error
	GetCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
	// UpdateCRD replaces an existing resource. Use it for objects fetched
	// via GetCRD (they carry a resourceVersion, which Create rejects).
	UpdateCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error
	DeleteCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error
	GetPods(ctx context.Context, namespace string, labelSelector string) ([]corev1.Pod, error)
	IsPodReady(ctx context.Context, namespace, name string) (bool, error)
	GetSecret(ctx context.Context, namespace, name string) (map[string][]byte, error)
	CreateSecret(ctx context.Context, namespace, name string, data map[string][]byte) error
	ExecInPod(ctx context.Context, namespace, pod, container string, cmd []string) (string, error)
	GetPodMetrics(ctx context.Context, namespace string) ([]PodResourceMetrics, error)
	ListNamespaces(ctx context.Context, prefix string) ([]string, error)
	ListCRDs(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error)
	ApplyManifestURL(ctx context.Context, url string) error
	GetDeployment(ctx context.Context, namespace, name string) (bool, error)
	InstallHelmChart(ctx context.Context, namespace, releaseName, chartPath string, values map[string]interface{}) error
	UninstallHelmChart(ctx context.Context, namespace, releaseName string) error

	// EnsureDenoRuntime idempotently creates the per-project Deno runtime
	// (Deployment + Service) in the given namespace. No-op if already present.
	// The runtime is reachable at http://deno-runtime.{namespace}.svc.cluster.local:8000
	// after the pod becomes ready (caller polls IsPodReady or sleeps).
	EnsureDenoRuntime(ctx context.Context, namespace string, spec DenoRuntimeSpec) error

	// GetClusterCapacity returns aggregate Allocatable + already-Requested
	// CPU/memory across all schedulable nodes. Used by capacity-aware
	// provisioning to refuse projects that wouldn't fit.
	GetClusterCapacity(ctx context.Context) (ClusterCapacity, error)
}

// ClusterCapacity holds aggregate cluster resource state. All values are in
// canonical Kubernetes resource.Quantity Milli/MilliBytes — i.e. CPU is in
// milliCPU (1000m = 1 core), memory is in bytes.
//
// Allocatable is what kubelet exposes (after kube-reserved + system-reserved).
// In practice we hold back a further percentage so the cluster never runs at
// 100% allocatable: that buffer covers monitoring/observability burst, brief
// pod-restart spikes, and lets node-level GC + kubelet stay responsive. The
// HeadroomPercent field captures that policy; Usable* methods apply it.
type ClusterCapacity struct {
	AllocatableCPUMilli int64 // sum of allocatable CPU across nodes (milli)
	AllocatableMemBytes int64 // sum of allocatable memory across nodes (bytes)
	RequestedCPUMilli   int64 // sum of pod CPU requests in flight (milli)
	RequestedMemBytes   int64 // sum of pod memory requests in flight (bytes)
	HeadroomPercent     int   // % of allocatable held back as a safety buffer (0-100)
}

// UsableCPUMilli is allocatable minus the headroom buffer. This is the
// number provisioning should plan against — never `Allocatable` directly.
func (c ClusterCapacity) UsableCPUMilli() int64 {
	if c.HeadroomPercent <= 0 || c.HeadroomPercent >= 100 {
		return c.AllocatableCPUMilli
	}
	return c.AllocatableCPUMilli * int64(100-c.HeadroomPercent) / 100
}

// UsableMemBytes is allocatable memory minus the headroom buffer.
func (c ClusterCapacity) UsableMemBytes() int64 {
	if c.HeadroomPercent <= 0 || c.HeadroomPercent >= 100 {
		return c.AllocatableMemBytes
	}
	return c.AllocatableMemBytes * int64(100-c.HeadroomPercent) / 100
}

// FreeCPUMilli is usable - requested. Negative means we're already past the
// safety threshold and new provisions should be refused.
func (c ClusterCapacity) FreeCPUMilli() int64 {
	return c.UsableCPUMilli() - c.RequestedCPUMilli
}

// FreeMemBytes is usable memory - requested.
func (c ClusterCapacity) FreeMemBytes() int64 {
	return c.UsableMemBytes() - c.RequestedMemBytes
}

// DenoRuntimeSpec configures the per-project Deno runtime pod. Resource
// limits scale with tier so paid projects get more headroom without cost to
// free/hobbyist projects.
type DenoRuntimeSpec struct {
	Image         string // container image, e.g. excalibase/deno-runtime:latest
	RuntimeSecret string // X-Runtime-Secret env var
	Tier          string // "FREE" (default), "STANDARD", "ENTERPRISE"
	// AllowedHosts is the project's outbound allowlist, already canonical
	// (edgefn.ParseEgressHosts), rendered as the runtime's ALLOWED_HOSTS env
	// and mirrored into the egress NetworkPolicy. Empty = no egress (EXC-348).
	AllowedHosts []string
}

// Verify Client implements KubeClient at compile time.
var _ KubeClient = (*Client)(nil)
