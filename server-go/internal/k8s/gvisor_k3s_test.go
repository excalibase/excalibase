//go:build live

package k8s

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	gvisorK3sImage     = "rancher/k3s:v1.31.6-k3s1"
	gvisorRelease      = "20260921.0"
	gvisorReleaseURL   = "https://storage.googleapis.com/gvisor/releases/release/" + gvisorRelease + "/x86_64/gvisor.tar.bz2"
	gvisorRuntimeClass = "gvisor"
	gvisorNodeLabel    = "excalibase.io/gvisor"

	gvisorPlatformSystrap = "systrap"
	gvisorPlatformKVM     = "kvm"
)

// k3s v1.31.6 ships containerd 2.0, which renders config-v3.toml.tmpl on top of its own base config.
const containerdRunscTemplate = `{{ template "base" . }}

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runsc]
  runtime_type = "io.containerd.runsc.v1"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runsc.options]
  TypeUrl = "io.containerd.runsc.v1.options"
  ConfigPath = "/etc/containerd/runsc.toml"
`

var gvisorPlatforms = []string{gvisorPlatformSystrap, gvisorPlatformKVM}

// gvisorCluster is a single-node k3s whose containerd offers a runsc handler
// and a RuntimeClass that schedules only onto the labelled node.
type gvisorCluster struct {
	container *k3s.K3sContainer
	clientset kubernetes.Interface
	client    *Client
	node      string
	platform  string
}

func startGVisorK3s(t *testing.T, platform string) *gvisorCluster {
	t.Helper()
	if platform == gvisorPlatformKVM {
		if _, err := os.Stat("/dev/kvm"); err != nil {
			t.Skipf("host has no /dev/kvm (%v); the KVM platform cannot be exercised here", err)
		}
	}
	ctx := context.Background()
	container, err := k3s.Run(ctx, gvisorK3sImage, testcontainers.WithFiles(gvisorFiles(t, platform)...))
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	cluster := &gvisorCluster{container: container, platform: platform}
	cluster.connect(t)
	if platform == gvisorPlatformKVM {
		cluster.nodeShell(t, "test -c /dev/kvm")
	}
	cluster.node = cluster.waitForNode(t)
	cluster.setGVisorLabel(t, true)
	cluster.createRuntimeClass(t)
	cluster.installCiliumPolicyKind(t)
	return cluster
}

func (g *gvisorCluster) connect(t *testing.T) {
	t.Helper()
	kubeconfig, err := g.container.GetKubeConfig(context.Background())
	if err != nil {
		t.Fatalf("get kubeconfig: %v", err)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	g.clientset = clientset
	g.client = &Client{clientset: clientset, dynamicClient: dyn, restConfig: restCfg}
}

// installCiliumPolicyKind registers the CiliumNetworkPolicy kind only, so the deploy path applies its
// fence; enforcement is TestK3sCiliumAppEgress's job.
func (g *gvisorCluster) installCiliumPolicyKind(t *testing.T) {
	t.Helper()
	crds := g.client.dynamicClient.Resource(schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"})
	crd := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]interface{}{"name": "ciliumnetworkpolicies.cilium.io"},
		"spec": map[string]interface{}{
			"group": "cilium.io",
			"scope": "Namespaced",
			"names": map[string]interface{}{
				"kind": "CiliumNetworkPolicy", "plural": "ciliumnetworkpolicies", "singular": "ciliumnetworkpolicy"},
			"versions": []interface{}{map[string]interface{}{
				"name": "v2", "served": true, "storage": true,
				"schema": map[string]interface{}{"openAPIV3Schema": map[string]interface{}{
					"type": "object", "x-kubernetes-preserve-unknown-fields": true}},
			}},
		},
	}}
	ctx := context.Background()
	if _, err := crds.Create(ctx, crd, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create CiliumNetworkPolicy CRD: %v", err)
	}
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if _, err := g.client.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace("default").
			List(ctx, metav1.ListOptions{}); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("the CiliumNetworkPolicy kind was never served")
}

func (g *gvisorCluster) waitForNode(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		nodes, err := g.clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
		if err == nil && len(nodes.Items) == 1 {
			return nodes.Items[0].Name
		}
		time.Sleep(time.Second)
	}
	t.Fatal("the k3s node never registered")
	return ""
}

func (g *gvisorCluster) setGVisorLabel(t *testing.T, present bool) {
	t.Helper()
	value := `null`
	if present {
		value = `"true"`
	}
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:%s}}}`, gvisorNodeLabel, value)
	_, err := g.clientset.CoreV1().Nodes().Patch(context.Background(), g.node,
		types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		t.Fatalf("label node: %v", err)
	}
}

func (g *gvisorCluster) createRuntimeClass(t *testing.T) {
	t.Helper()
	runtimeClass := &nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: gvisorRuntimeClass},
		Handler:    "runsc",
		Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{gvisorNodeLabel: "true"}},
	}
	if _, err := g.clientset.NodeV1().RuntimeClasses().Create(context.Background(), runtimeClass, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create runtime class: %v", err)
	}
}

func (g *gvisorCluster) createNamespace(t *testing.T, name string) string {
	t.Helper()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if _, err := g.clientset.CoreV1().Namespaces().Create(context.Background(), namespace, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	return name
}

// nodeShell runs a shell script on the k3s node itself and fails the test on a non-zero exit.
func (g *gvisorCluster) nodeShell(t *testing.T, script string) string {
	t.Helper()
	code, reader, err := g.container.Exec(context.Background(), []string{"sh", "-c", script}, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("node exec %q: %v", script, err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("node exec %q: read output: %v", script, err)
	}
	if code != 0 {
		t.Fatalf("node exec %q exited %d: %s", script, code, output)
	}
	return string(output)
}

func liveApp(name, image string, port int) *apphost.App {
	return &apphost.App{ID: "app-" + name, ProjectID: "proj1", Name: name, Image: image,
		Port: port, Replicas: 1, Tier: domain.Free, Status: apphost.StatusCreated}
}

// deployApp drives the production path: render, apply, wait.
func (g *gvisorCluster) deployApp(t *testing.T, namespace string, app *apphost.App, timeout time.Duration) error {
	t.Helper()
	workload, err := RenderAppWorkload(namespace, app, nil, AppRenderOptions{RuntimeClass: gvisorRuntimeClass})
	if err != nil {
		t.Fatalf("render %s: %v", app.Name, err)
	}
	ctx := context.Background()
	if err := g.client.ApplyAppWorkload(ctx, namespace, workload); err != nil {
		t.Fatalf("apply %s: %v", app.Name, err)
	}
	return g.client.WaitForAppRollout(ctx, namespace, AppObjectName(app.Name), timeout)
}

// appPod returns the newest live pod of the app.
func (g *gvisorCluster) appPod(t *testing.T, namespace, name string) corev1.Pod {
	t.Helper()
	pods, err := g.clientset.CoreV1().Pods(namespace).List(context.Background(),
		metav1.ListOptions{LabelSelector: "excalibase.io/app=app-" + name})
	if err != nil {
		t.Fatalf("list pods of %s: %v", name, err)
	}
	var newest *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp == nil && (newest == nil || pod.CreationTimestamp.After(newest.CreationTimestamp.Time)) {
			newest = pod
		}
	}
	if newest == nil {
		t.Fatalf("no live pod for %s", name)
	}
	return *newest
}

func (g *gvisorCluster) exec(t *testing.T, pod corev1.Pod, script string) string {
	t.Helper()
	output, err := g.client.ExecInPod(context.Background(), pod.Namespace, pod.Name,
		pod.Spec.Containers[0].Name, []string{"sh", "-c", script})
	if err != nil {
		t.Fatalf("exec %q in %s: %v", script, pod.Name, err)
	}
	return output
}

func gvisorFiles(t *testing.T, platform string) []testcontainers.ContainerFile {
	t.Helper()
	dir := fetchGVisor(t)
	files := []testcontainers.ContainerFile{
		{HostFilePath: filepath.Join(dir, "runsc"), ContainerFilePath: "/bin/runsc", FileMode: 0o755},
		{HostFilePath: filepath.Join(dir, "containerd-shim-runsc-v1"), ContainerFilePath: "/bin/containerd-shim-runsc-v1", FileMode: 0o755},
		{
			Reader:            strings.NewReader(containerdRunscTemplate),
			ContainerFilePath: "/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.tmpl",
			FileMode:          0o644,
		},
		{
			Reader:            strings.NewReader(fmt.Sprintf("[runsc_config]\n  platform = %q\n", platform)),
			ContainerFilePath: "/etc/containerd/runsc.toml",
			FileMode:          0o644,
		},
	}
	// runsc refuses to start a sandbox without its sidecar binaries beside it.
	sidecars, err := os.ReadDir(filepath.Join(dir, "gvisor-bin"))
	if err != nil {
		t.Fatalf("read gvisor sidecars: %v", err)
	}
	for _, sidecar := range sidecars {
		files = append(files, testcontainers.ContainerFile{
			HostFilePath:      filepath.Join(dir, "gvisor-bin", sidecar.Name()),
			ContainerFilePath: "/bin/gvisor-bin/" + sidecar.Name(),
			FileMode:          0o755,
		})
	}
	return files
}

// fetchGVisor downloads the pinned official release once, verifies its
// published sha512, and caches the extracted binaries.
func fetchGVisor(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("user cache dir: %v", err)
	}
	dir := filepath.Join(cache, "excalibase-gvisor", gvisorRelease)
	if _, err := os.Stat(filepath.Join(dir, ".verified")); err == nil {
		return dir
	}
	if err := downloadGVisor(dir); err != nil {
		t.Fatalf("fetch gVisor %s: %v", gvisorRelease, err)
	}
	return dir
}

func downloadGVisor(dir string) error {
	want, err := publishedSHA512()
	if err != nil {
		return err
	}
	archive, err := os.CreateTemp("", "gvisor-*.tar.bz2")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	got, err := downloadHashed(gvisorReleaseURL, archive)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("sha512 mismatch: got %s, published %s", got, want)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), "staging-")
	if err != nil {
		return err
	}
	if err := extractGVisor(archive, staging); err != nil {
		os.RemoveAll(staging)
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, ".verified"), []byte(want), 0o644); err != nil {
		return err
	}
	return os.Rename(staging, dir)
}

func publishedSHA512() (string, error) {
	var published strings.Builder
	if _, err := downloadHashed(gvisorReleaseURL+".sha512", &published); err != nil {
		return "", err
	}
	fields := strings.Fields(published.String())
	if len(fields) == 0 {
		return "", errors.New("empty sha512 file")
	}
	return fields[0], nil
}

func downloadHashed(url string, into io.Writer) (string, error) {
	response, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, response.Status)
	}
	hash := sha512.New()
	if _, err := io.Copy(io.MultiWriter(into, hash), response.Body); err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractGVisor(archive io.Reader, dir string) error {
	entries := tar.NewReader(bzip2.NewReader(archive))
	for {
		header, err := entries.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeReg && wantedGVisorFile(header.Name) {
			if err := writeExecutable(dir, header.Name, entries); err != nil {
				return err
			}
		}
	}
}

func wantedGVisorFile(name string) bool {
	if name == "runsc" || name == "containerd-shim-runsc-v1" {
		return true
	}
	return strings.HasPrefix(name, "gvisor-bin/") && !strings.Contains(name, "..") && strings.Count(name, "/") == 1
}

func writeExecutable(dir, name string, content io.Reader) error {
	root := filepath.Clean(dir)
	path := filepath.Join(root, name)
	if !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return fmt.Errorf("archive entry %q escapes %s", name, root)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, content); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
