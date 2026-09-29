//go:build live

package k8s

import (
	"context"
	"fmt"
	"io"
	"os"
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
	"github.com/excalibase/provisioning-poc/internal/testutil/gvisortest"
)

const (
	gvisorK3sImage     = "rancher/k3s:v1.31.6-k3s1"
	gvisorRuntimeClass = "gvisor"
	gvisorNodeLabel    = "excalibase.io/gvisor"

	gvisorPlatformSystrap = "systrap"
	gvisorPlatformKVM     = "kvm"
)

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
	g.client = &Client{clientset: clientset, dynamicClient: dyn, restConfig: restCfg, projectAccess: testAccess}
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
	workload, err := RenderAppWorkload(namespace, app, nil, AppRenderOptions{RuntimeClass: gvisorRuntimeClass, Route: liveRoute, DeployID: liveDeployID})
	if err != nil {
		t.Fatalf("render %s: %v", app.Name, err)
	}
	ctx := context.Background()
	if err := g.client.ApplyAppWorkload(ctx, namespace, workload); err != nil {
		t.Fatalf("apply %s: %v", app.Name, err)
	}
	return g.client.WaitForAppRollout(ctx, namespace, AppObjectName(app.Name), liveDeployID, timeout)
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
	return gvisortest.Files(t, platform)
}
