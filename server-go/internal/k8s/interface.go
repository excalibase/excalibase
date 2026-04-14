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
}

// DenoRuntimeSpec configures the per-project Deno runtime pod. Resource
// limits scale with tier so paid projects get more headroom without cost to
// free/hobbyist projects.
type DenoRuntimeSpec struct {
	Image         string // container image, e.g. excalibase/deno-runtime:latest
	RuntimeSecret string // X-Runtime-Secret env var
	Tier          string // "FREE" (default), "STANDARD", "ENTERPRISE"
}

// Verify Client implements KubeClient at compile time.
var _ KubeClient = (*Client)(nil)
