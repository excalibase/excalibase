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
}

// Verify Client implements KubeClient at compile time.
var _ KubeClient = (*Client)(nil)
