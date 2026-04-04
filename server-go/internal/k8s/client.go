package k8s

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/homedir"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Client wraps client-go for K8s operations.
type Client struct {
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface
	metricsClient *metricsv.Clientset
	restConfig    *rest.Config
}

func NewClient() (*Client, error) {
	config, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	metricsClient, err := metricsv.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create metrics client: %w", err)
	}

	return &Client{
		clientset:     clientset,
		dynamicClient: dynClient,
		metricsClient: metricsClient,
		restConfig:    config,
	}, nil
}

func loadConfig() (*rest.Config, error) {
	// Try in-cluster first
	if config, err := rest.InClusterConfig(); err == nil {
		return config, nil
	}
	// Fall back to kubeconfig
	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

// CreateNamespace creates a K8s namespace.
func (c *Client) CreateNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	_, err := c.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return err
}

// DeleteNamespace deletes a K8s namespace.
func (c *Client) DeleteNamespace(ctx context.Context, name string) error {
	return c.clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
}

// ApplyCRD creates or updates an unstructured CRD resource.
func (c *Client) ApplyCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error {
	_, err := c.dynamicClient.Resource(gvr).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil && strings.Contains(err.Error(), "already exists") {
		_, err = c.dynamicClient.Resource(gvr).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
	}
	return err
}

// GetCRD fetches an unstructured CRD resource.
func (c *Client) GetCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return c.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// DeleteCRD deletes a CRD resource.
func (c *Client) DeleteCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	return c.dynamicClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// GetPods lists pods in a namespace with optional label selector.
func (c *Client) GetPods(ctx context.Context, namespace string, labelSelector string) ([]corev1.Pod, error) {
	opts := metav1.ListOptions{}
	if labelSelector != "" {
		opts.LabelSelector = labelSelector
	}
	list, err := c.clientset.CoreV1().Pods(namespace).List(ctx, opts)
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// IsPodReady checks if a pod has phase=Running and all containers ready.
func (c *Client) IsPodReady(ctx context.Context, namespace, name string) (bool, error) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if pod.Status.Phase != corev1.PodRunning {
		return false, nil
	}
	for _, c := range pod.Status.ContainerStatuses {
		if !c.Ready {
			return false, nil
		}
	}
	return true, nil
}

// GetSecret reads a K8s secret.
func (c *Client) GetSecret(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	secret, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return secret.Data, nil
}

// CreateSecret creates a K8s secret.
func (c *Client) CreateSecret(ctx context.Context, namespace, name string, data map[string][]byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       data,
	}
	_, err := c.clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	return err
}

// ExecInPod runs a command inside a pod and returns stdout.
func (c *Client) ExecInPod(ctx context.Context, namespace, pod, container string, cmd []string) (string, error) {
	req := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		Param("container", container).
		Param("stdout", "true").
		Param("stderr", "true")

	for _, c := range cmd {
		req = req.Param("command", c)
	}

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return "", fmt.Errorf("exec: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.String(), nil
}

// GetPodMetrics returns CPU/memory for all pods in a namespace via metrics-server.
func (c *Client) GetPodMetrics(ctx context.Context, namespace string) ([]PodResourceMetrics, error) {
	list, err := c.metricsClient.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod metrics: %w", err)
	}

	var result []PodResourceMetrics
	for _, pm := range list.Items {
		var cpuMillis int64
		var memBytes int64
		for _, container := range pm.Containers {
			cpuMillis += container.Usage.Cpu().MilliValue()
			memBytes += container.Usage.Memory().Value()
		}
		result = append(result, PodResourceMetrics{
			Name:      pm.Name,
			CPUMillis: cpuMillis,
			MemoryMB:  memBytes / (1024 * 1024),
		})
	}
	return result, nil
}

type PodResourceMetrics struct {
	Name      string
	CPUMillis int64
	MemoryMB  int64
}

// ListNamespaces returns namespace names matching the given prefix.
func (c *Client) ListNamespaces(ctx context.Context, prefix string) ([]string, error) {
	list, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	var result []string
	for _, ns := range list.Items {
		if prefix == "" || strings.HasPrefix(ns.Name, prefix) {
			result = append(result, ns.Name)
		}
	}
	return result, nil
}

// ListCRDs lists all custom resources of the given GVR in a namespace.
func (c *Client) ListCRDs(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	list, err := c.dynamicClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list CRDs: %w", err)
	}
	var result []*unstructured.Unstructured
	for i := range list.Items {
		result = append(result, &list.Items[i])
	}
	return result, nil
}

// ApplyManifestURL downloads a YAML manifest from url and applies each document
// using server-side apply via the dynamic client.
func (c *Client) ApplyManifestURL(ctx context.Context, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download manifest: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read manifest body: %w", err)
	}

	reader := yamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(body)))
	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read YAML document: %w", err)
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		jsonData, err := yamlutil.ToJSON(doc)
		if err != nil {
			return fmt.Errorf("convert YAML to JSON: %w", err)
		}

		var obj unstructured.Unstructured
		if err := json.Unmarshal(jsonData, &obj.Object); err != nil {
			return fmt.Errorf("unmarshal object: %w", err)
		}

		gvk := obj.GroupVersionKind()
		gvr := schema.GroupVersionResource{
			Group:    gvk.Group,
			Version:  gvk.Version,
			Resource: strings.ToLower(gvk.Kind) + "s",
		}

		ns := obj.GetNamespace()
		var resource dynamic.ResourceInterface
		if ns != "" {
			resource = c.dynamicClient.Resource(gvr).Namespace(ns)
		} else {
			resource = c.dynamicClient.Resource(gvr)
		}

		obj.SetManagedFields(nil)
		_, err = resource.Apply(ctx, obj.GetName(), &obj, metav1.ApplyOptions{
			FieldManager: "excalibase-server",
			Force:        true,
		})
		if err != nil {
			return fmt.Errorf("apply %s/%s: %w", gvr.Resource, obj.GetName(), err)
		}
	}

	return nil
}

// GetDeployment checks whether a deployment exists in the given namespace.
// Returns true if found, false if not found.
func (c *Client) GetDeployment(ctx context.Context, namespace, name string) (bool, error) {
	_, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	return true, nil
}
