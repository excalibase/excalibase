package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	testNS        = "test-ns"
	testDelNS     = "del-ns"
	testSecNS     = "sec-ns"
	testGroup     = "test.io"
	testAPIVersion = "test.io/v1"
)


// newFakeClient builds a Client backed by in-memory fakes (no real K8s needed).
func newFakeClient(objects ...runtime.Object) *Client {
	cs := fake.NewSimpleClientset(objects...)
	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClient(scheme)

	return &Client{
		clientset:     cs,
		dynamicClient: dynClient,
		// metricsClient and restConfig left nil — tested separately
	}
}

// --- Namespace tests ---

func TestClientCreateNamespace(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()

	if err := c.CreateNamespace(ctx, testNS); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}

	ns, err := c.clientset.CoreV1().Namespaces().Get(ctx, testNS, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace should exist: %v", err)
	}
	if ns.Name != testNS {
		t.Errorf("name: got %s", ns.Name)
	}
}

func TestClientCreateNamespaceDuplicate(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()

	c.CreateNamespace(ctx, "dup-ns")
	err := c.CreateNamespace(ctx, "dup-ns")
	if err == nil {
		t.Error("expected error for duplicate namespace")
	}
}

func TestClientDeleteNamespace(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()

	c.CreateNamespace(ctx, testDelNS)
	if err := c.DeleteNamespace(ctx, testDelNS); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}

	_, err := c.clientset.CoreV1().Namespaces().Get(ctx, testDelNS, metav1.GetOptions{})
	if err == nil {
		t.Error("namespace should be deleted")
	}
}

// --- Secret tests ---

func TestClientCreateAndGetSecret(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()

	c.CreateNamespace(ctx, testSecNS)
	err := c.CreateSecret(ctx, testSecNS, "my-secret", map[string][]byte{
		"username": []byte("app"),
		"password": []byte("s3cret"),
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	data, err := c.GetSecret(ctx, testSecNS, "my-secret")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if string(data["username"]) != "app" {
		t.Errorf("username: got %s", data["username"])
	}
	if string(data["password"]) != "s3cret" {
		t.Errorf("password: got %s", data["password"])
	}
}

func TestClientGetSecretNotFound(t *testing.T) {
	c := newFakeClient()
	_, err := c.GetSecret(context.Background(), "ns", "nonexistent")
	if err == nil {
		t.Error("expected error for missing secret")
	}
}

// --- Pod tests ---

func TestClientGetPods(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "db-postgres-1", Namespace: testNS,
			Labels: map[string]string{"app": "postgres"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	c := newFakeClient(pod)

	pods, err := c.GetPods(context.Background(), testNS, "app=postgres")
	if err != nil {
		t.Fatalf("GetPods: %v", err)
	}
	if len(pods) != 1 {
		t.Errorf("expected 1 pod, got %d", len(pods))
	}
}

func TestClientGetPodsEmpty(t *testing.T) {
	c := newFakeClient()
	pods, err := c.GetPods(context.Background(), "empty-ns", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 0 {
		t.Errorf("expected 0 pods, got %d", len(pods))
	}
}

func TestClientIsPodReadyRunning(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ready-pod", Namespace: "ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "postgres", Ready: true},
			},
		},
	}
	c := newFakeClient(pod)

	ready, err := c.IsPodReady(context.Background(), "ns", "ready-pod")
	if err != nil {
		t.Fatalf("IsPodReady: %v", err)
	}
	if !ready {
		t.Error("pod should be ready")
	}
}

func TestClientIsPodReadyPending(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-pod", Namespace: "ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	c := newFakeClient(pod)

	ready, _ := c.IsPodReady(context.Background(), "ns", "pending-pod")
	if ready {
		t.Error("pending pod should not be ready")
	}
}

func TestClientIsPodReadyContainerNotReady(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "unready-pod", Namespace: "ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "postgres", Ready: false},
			},
		},
	}
	c := newFakeClient(pod)

	ready, _ := c.IsPodReady(context.Background(), "ns", "unready-pod")
	if ready {
		t.Error("pod with unready container should not be ready")
	}
}

func TestClientIsPodReadyNotFound(t *testing.T) {
	c := newFakeClient()
	_, err := c.IsPodReady(context.Background(), "ns", "ghost-pod")
	if err == nil {
		t.Error("expected error for missing pod")
	}
}

// --- CRD tests (dynamic fake client) ---

func TestClientApplyCRD(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()

	gvr := schema.GroupVersionResource{Group: testGroup, Version: "v1", Resource: "widgets"}
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": testAPIVersion,
			"kind":       "Widget",
			"metadata":   map[string]interface{}{"name": "w1", "namespace": "ns"},
			"spec":       map[string]interface{}{"size": "large"},
		},
	}

	if err := c.ApplyCRD(ctx, gvr, "ns", obj); err != nil {
		t.Fatalf("ApplyCRD create: %v", err)
	}

	got, err := c.GetCRD(ctx, gvr, "ns", "w1")
	if err != nil {
		t.Fatalf("GetCRD: %v", err)
	}
	spec := got.Object["spec"].(map[string]interface{})
	if spec["size"] != "large" {
		t.Errorf("spec.size: got %v", spec["size"])
	}
}

func TestClientApplyCRDUpdate(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()

	gvr := schema.GroupVersionResource{Group: testGroup, Version: "v1", Resource: "widgets"}
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": testAPIVersion,
			"kind":       "Widget",
			"metadata":   map[string]interface{}{"name": "w1", "namespace": "ns"},
			"spec":       map[string]interface{}{"size": "small"},
		},
	}

	c.ApplyCRD(ctx, gvr, "ns", obj)

	// Update
	obj.Object["spec"] = map[string]interface{}{"size": "xl"}
	if err := c.ApplyCRD(ctx, gvr, "ns", obj); err != nil {
		t.Fatalf("ApplyCRD update: %v", err)
	}

	got, _ := c.GetCRD(ctx, gvr, "ns", "w1")
	spec := got.Object["spec"].(map[string]interface{})
	if spec["size"] != "xl" {
		t.Errorf("updated spec.size: got %v", spec["size"])
	}
}

func TestClientDeleteCRD(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()

	gvr := schema.GroupVersionResource{Group: testGroup, Version: "v1", Resource: "things"}
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": testAPIVersion,
			"kind":       "Thing",
			"metadata":   map[string]interface{}{"name": "t1", "namespace": "ns"},
		},
	}
	c.ApplyCRD(ctx, gvr, "ns", obj)

	if err := c.DeleteCRD(ctx, gvr, "ns", "t1"); err != nil {
		t.Fatalf("DeleteCRD: %v", err)
	}

	_, err := c.GetCRD(ctx, gvr, "ns", "t1")
	if err == nil {
		t.Error("CRD should be deleted")
	}
}

func TestClientGetCRDNotFound(t *testing.T) {
	c := newFakeClient()
	gvr := schema.GroupVersionResource{Group: "x.io", Version: "v1", Resource: "xs"}
	_, err := c.GetCRD(context.Background(), gvr, "ns", "nope")
	if err == nil {
		t.Error("expected error for missing CRD")
	}
}
