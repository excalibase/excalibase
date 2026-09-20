package k8s

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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

// ClientOptions configures how NewClientWith builds a K8s client. All fields
// are optional — if none are set, NewClientWith behaves like NewClient and
// tries in-cluster → default kubeconfig.
type ClientOptions struct {
	// KubeconfigPath points at a kubeconfig file (e.g. /home/user/.kube/config
	// or a mounted secret). If set, takes precedence over remote API fields.
	KubeconfigPath string
	// APIURL + BearerToken + CACert together configure a remote API connection
	// — Jenkins-style, for platform-runs-outside-cluster deployments.
	APIURL      string
	BearerToken string
	// CACertPEM is the PEM-encoded CA bundle for the remote API's TLS.
	// Leave empty to skip TLS verification (not recommended for production).
	CACertPEM []byte
	// InsecureSkipTLSVerify disables TLS verification on the remote API.
	// Only set for local development against clusters with self-signed certs.
	InsecureSkipTLSVerify bool
}

func NewClient() (*Client, error) {
	return NewClientWith(ClientOptions{})
}

// NewClientWith builds a K8s client using the given options. Priority order:
//  1. opts.APIURL + opts.BearerToken set → remote API mode
//  2. opts.KubeconfigPath set → load that kubeconfig file
//  3. KUBECONFIG env var set → load that file
//  4. In-cluster config (ServiceAccount auto-mount)
//  5. ~/.kube/config (developer fallback)
func NewClientWith(opts ClientOptions) (*Client, error) {
	config, err := loadConfigWith(opts)
	if err != nil {
		return nil, fmt.Errorf("load k8s config: %w", err)
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

func loadConfigWith(opts ClientOptions) (*rest.Config, error) {
	// 1. Remote API + bearer token (Jenkins-style)
	if opts.APIURL != "" && opts.BearerToken != "" {
		cfg := &rest.Config{
			Host:        opts.APIURL,
			BearerToken: opts.BearerToken,
		}
		if opts.InsecureSkipTLSVerify {
			cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: true}
		} else if len(opts.CACertPEM) > 0 {
			cfg.TLSClientConfig = rest.TLSClientConfig{CAData: opts.CACertPEM}
		}
		return cfg, nil
	}
	// 2. Explicit kubeconfig path from options
	if opts.KubeconfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", opts.KubeconfigPath)
	}
	// 3. KUBECONFIG env var
	if envPath := os.Getenv("KUBECONFIG"); envPath != "" {
		return clientcmd.BuildConfigFromFlags("", envPath)
	}
	// 4. In-cluster (ServiceAccount)
	if config, err := rest.InClusterConfig(); err == nil {
		return config, nil
	}
	// 5. Developer fallback
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

// CreateNamespaceWithLabels creates a K8s namespace with the given labels and
// fences it with a default-deny ingress policy (EXC-325) so one tenant's pods
// cannot reach another tenant's pods.
func (c *Client) CreateNamespaceWithLabels(ctx context.Context, name string, labels map[string]string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
	if _, err := c.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		return err
	}
	if err := c.ensureNamespaceIsolationPolicy(ctx, name); err != nil {
		return err
	}
	return c.ensureNamespaceQuota(ctx, name)
}

// ensureNamespaceQuota caps the total object COUNT a tenant namespace may hold
// (EXC-329) — a hard ceiling so one tenant can't balloon the shared box by
// spawning many pods/PVCs (e.g. runaway edge-function deploys). Deliberately
// count-based, NOT a CPU/memory-request quota: per-pod CPU/RAM is already capped
// from the tier (CNPG cluster resources + deno tier resources), and a
// request-based quota would reject CNPG's transient backup/maintenance pods that
// don't declare requests. Generous limits: a healthy tenant runs ~3-5 pods
// (Postgres + watcher + deno), so 20 leaves ample headroom for CNPG jobs.
func (c *Client) ensureNamespaceQuota(ctx context.Context, namespace string) error {
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "namespace-quota",
			Namespace: namespace,
			Labels:    map[string]string{"excalibase.io/component": "quota"},
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourcePods:                   resource.MustParse("20"),
				corev1.ResourcePersistentVolumeClaims: resource.MustParse("6"),
				corev1.ResourceServices:               resource.MustParse("15"),
			},
		},
	}
	_, err := c.clientset.CoreV1().ResourceQuotas(namespace).Create(ctx, quota, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace quota: %w", err)
	}
	return nil
}

// ensureNamespaceIsolationPolicy applies a default-deny INGRESS policy to every
// pod in a project namespace, opening it only to traffic that legitimately
// crosses in (EXC-325). Without it every project namespace is on a flat network
// and a compromised pod in tenant A reaches tenant B's database directly.
//
// Allowed sources:
//   - same namespace  (postgres replication, watcher→pg, deno→pg)
//   - the platform namespace (provisioning → deno /deploy /invoke, and the
//     schema handler's direct connection to the project's Postgres)
//   - cnpg-system (the CloudNativePG operator managing the Postgres cluster)
//   - monitoring (Prometheus scraping, when observability is enabled)
//
// Everything else — notably every OTHER {org}-{project} namespace — is denied by
// omission. Egress is untouched (the Deno pod's egress is fenced separately by
// ensureDenoEgressPolicy). Kubelet health probes are node-local and bypass
// NetworkPolicy, so readiness/liveness are unaffected. Requires a
// policy-enforcing CNI (Calico/Cilium); a CNI that ignores policies creates the
// object without enforcing it.
func (c *Client) ensureNamespaceIsolationPolicy(ctx context.Context, namespace string) error {
	nsPeer := func(n string) networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"kubernetes.io/metadata.name": n},
			},
		}
	}
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "namespace-isolation",
			Namespace: namespace,
			Labels:    map[string]string{"excalibase.io/component": "isolation"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			// All pods in the namespace.
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{
					// Same namespace: empty PodSelector, no NamespaceSelector.
					{PodSelector: &metav1.LabelSelector{}},
					nsPeer(platformNamespace()),
					nsPeer("cnpg-system"),
					nsPeer("monitoring"),
				},
			}},
		},
	}
	_, err := c.clientset.NetworkingV1().NetworkPolicies(namespace).Create(ctx, policy, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace isolation policy: %w", err)
	}
	return nil
}

// DeleteNamespace deletes a K8s namespace. An already-absent namespace is a
// success so a retried teardown is idempotent.
func (c *Client) DeleteNamespace(ctx context.Context, name string) error {
	err := c.clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// NamespaceExists reports whether the namespace object is still present. A
// namespace in Terminating is still present — that is the point.
func (c *Client) NamespaceExists(ctx context.Context, name string) (bool, error) {
	_, err := c.clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get namespace %s: %w", name, err)
	}
	return true, nil
}

// ListPVCs returns the names of the PersistentVolumeClaims in a namespace. A
// namespace that is already gone holds no claims.
func (c *Client) ListPVCs(ctx context.Context, namespace string) ([]string, error) {
	list, err := c.clientset.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list pvcs in %s: %w", namespace, err)
	}
	names := make([]string, 0, len(list.Items))
	for _, pvc := range list.Items {
		names = append(names, pvc.Name)
	}
	return names, nil
}

// ApplyCRD creates or updates an unstructured CRD resource.
func (c *Client) ApplyCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error {
	_, err := c.dynamicClient.Resource(gvr).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil && strings.Contains(err.Error(), "already exists") {
		_, err = c.dynamicClient.Resource(gvr).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
	}
	return err
}

// UpdateCRD replaces an existing unstructured CRD resource, keeping the
// optimistic-concurrency check on the object's resourceVersion.
func (c *Client) UpdateCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error {
	_, err := c.dynamicClient.Resource(gvr).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

// GetCRD fetches an unstructured CRD resource.
func (c *Client) GetCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return c.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// DeleteCRD deletes a CRD resource. An already-absent resource is a success
// so a retried teardown is idempotent.
func (c *Client) DeleteCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	err := c.dynamicClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// CRDExists reports whether the named resource is still present.
func (c *Client) CRDExists(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (bool, error) {
	_, err := c.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get %s %s/%s: %w", gvr.Resource, namespace, name, err)
	}
	return true, nil
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
	return c.exec(ctx, namespace, pod, container, cmd, "")
}

// ExecInPodStdin runs a command inside a pod with stdin attached. Input given
// here stays in the request body; anything passed in cmd becomes a URL query
// parameter and is recorded in the API server's audit log.
func (c *Client) ExecInPodStdin(ctx context.Context, namespace, pod, container string, cmd []string, stdin string) (string, error) {
	return c.exec(ctx, namespace, pod, container, cmd, stdin)
}

func (c *Client) exec(ctx context.Context, namespace, pod, container string, cmd []string, stdin string) (string, error) {
	req := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		Param("container", container).
		Param("stdout", "true").
		Param("stderr", "true")
	if stdin != "" {
		req = req.Param("stdin", "true")
	}

	for _, c := range cmd {
		req = req.Param("command", c)
	}

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	opts := remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}
	if stdin != "" {
		opts.Stdin = strings.NewReader(stdin)
	}
	err = exec.StreamWithContext(ctx, opts)
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
		if err := c.applyYAMLDoc(ctx, doc); err != nil {
			return err
		}
	}
	return nil
}

// applyYAMLDoc converts a single YAML document to JSON, unmarshals it, and applies
// it to the cluster using server-side apply.
func (c *Client) applyYAMLDoc(ctx context.Context, doc []byte) error {
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

// GetClusterCapacity walks all schedulable nodes for Allocatable CPU/memory,
// then walks every non-terminal pod cluster-wide to sum CPU/memory requests.
// Cordoned/unschedulable nodes are skipped; pods in Succeeded/Failed phases
// are excluded since they no longer hold reservations.
func (c *Client) GetClusterCapacity(ctx context.Context) (ClusterCapacity, error) {
	var cap ClusterCapacity

	if err := c.accumulateNodeCapacity(ctx, &cap); err != nil {
		return cap, err
	}
	if err := c.accumulatePodRequests(ctx, &cap); err != nil {
		return cap, err
	}
	return cap, nil
}

// accumulateNodeCapacity adds Allocatable CPU/memory for each schedulable node.
func (c *Client) accumulateNodeCapacity(ctx context.Context, cap *ClusterCapacity) error {
	nodes, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	for _, n := range nodes.Items {
		if n.Spec.Unschedulable {
			continue
		}
		if cpu, ok := n.Status.Allocatable[corev1.ResourceCPU]; ok {
			cap.AllocatableCPUMilli += cpu.MilliValue()
		}
		if mem, ok := n.Status.Allocatable[corev1.ResourceMemory]; ok {
			cap.AllocatableMemBytes += mem.Value()
		}
	}
	return nil
}

// accumulatePodRequests sums CPU/memory requests for all non-terminal pods cluster-wide.
func (c *Client) accumulatePodRequests(ctx context.Context, cap *ClusterCapacity) error {
	pods, err := c.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, ctr := range p.Spec.Containers {
			if cpu, ok := ctr.Resources.Requests[corev1.ResourceCPU]; ok {
				cap.RequestedCPUMilli += cpu.MilliValue()
			}
			if mem, ok := ctr.Resources.Requests[corev1.ResourceMemory]; ok {
				cap.RequestedMemBytes += mem.Value()
			}
		}
	}
	return nil
}
