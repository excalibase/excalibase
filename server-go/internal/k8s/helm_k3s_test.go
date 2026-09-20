//go:build integration

// Helm SDK against live k3s via testcontainers — see k3s_test.go for the
// hang risk that motivated the build tag.

package k8s

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/k3s"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	testK3SStartMsg   = "Starting k3s container..."
	testK3SImage      = "rancher/k3s:v1.31.6-k3s1"
	testK3SStartFmt   = "k3s start: %v"
	testKubeconfigFmt = "get kubeconfig: %v"
	testHelmE2E       = "helm-e2e"
)

// Run with: go test ./internal/k8s/ -run TestK3sHelmInstallUninstall -v -timeout 5m
// Requires: Docker running.

func TestK3sHelmInstallUninstall(t *testing.T) {
	ctx := context.Background()

	// Find the test chart path (relative to this test file)
	chartPath := filepath.Join("testdata", "test-chart")
	if _, err := os.Stat(chartPath); err != nil {
		t.Fatalf("test chart not found at %s: %v", chartPath, err)
	}

	// Start k3s
	t.Log(testK3SStartMsg)
	container, err := k3s.Run(ctx, testK3SImage)
	if err != nil {
		t.Fatalf(testK3SStartFmt, err)
	}
	defer container.Terminate(ctx)

	// Get kubeconfig and write to temp file (Helm SDK needs file path)
	kubeconfig, err := container.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf(testKubeconfigFmt, err)
	}

	kubeconfigFile := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfigFile, kubeconfig, 0600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	t.Setenv("KUBECONFIG", kubeconfigFile)

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	cs, _ := kubernetes.NewForConfig(restCfg)
	dynClient, _ := dynamic.NewForConfig(restCfg)

	client := &Client{
		clientset:     cs,
		dynamicClient: dynClient,
		restConfig:    restCfg,
	}

	ns := "helm-test"

	// Create namespace
	if err := client.CreateNamespace(ctx, ns); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}

	// --- Test: Install chart ---
	t.Log("Installing test chart...")
	values := map[string]interface{}{
		"replicaCount": 1,
		"name":         testHelmE2E,
		"image":        "busybox:1.36",
	}

	if err := client.InstallHelmChart(ctx, ns, "my-release", chartPath, values); err != nil {
		t.Fatalf("InstallHelmChart: %v", err)
	}

	waitForHelmPod(ctx, t, client, ns)

	exists, err := client.GetDeployment(ctx, ns, testHelmE2E)
	if err != nil {
		t.Errorf("GetDeployment: %v", err)
	}
	if !exists {
		t.Error("deployment helm-e2e should exist after install")
	}

	t.Log("Uninstalling chart...")
	if err := client.UninstallHelmChart(ctx, ns, "my-release"); err != nil {
		t.Fatalf("UninstallHelmChart: %v", err)
	}

	time.Sleep(3 * time.Second)
	exists, err = client.GetDeployment(ctx, ns, testHelmE2E)
	if err == nil && exists {
		t.Error("deployment should be deleted after uninstall")
	}

	client.DeleteNamespace(ctx, ns)
	t.Log("Helm install/uninstall E2E test passed")
}

// waitForHelmPod polls until at least one pod with label app=helm-e2e appears.
func waitForHelmPod(ctx context.Context, t *testing.T, client *Client, ns string) {
	t.Helper()
	t.Log("Verifying deployment...")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		pods, _ := client.GetPods(ctx, ns, "app=helm-e2e")
		if len(pods) > 0 {
			t.Logf("Pod found: %s (phase: %s)", pods[0].Name, pods[0].Status.Phase)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Error("no pods found after helm install")
}

func TestK3sHelmInstallWithOverrides(t *testing.T) {
	ctx := context.Background()

	chartPath := filepath.Join("testdata", "test-chart")
	if _, err := os.Stat(chartPath); err != nil {
		t.Fatalf("test chart not found: %v", err)
	}

	t.Log(testK3SStartMsg)
	container, err := k3s.Run(ctx, testK3SImage)
	if err != nil {
		t.Fatalf(testK3SStartFmt, err)
	}
	defer container.Terminate(ctx)

	kubeconfig, err := container.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf(testKubeconfigFmt, err)
	}

	kubeconfigFile := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kubeconfigFile, kubeconfig, 0600)
	t.Setenv("KUBECONFIG", kubeconfigFile)

	restCfg, _ := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	cs, _ := kubernetes.NewForConfig(restCfg)
	dynClient, _ := dynamic.NewForConfig(restCfg)

	client := &Client{
		clientset:     cs,
		dynamicClient: dynClient,
		restConfig:    restCfg,
	}

	ns := "helm-override"
	client.CreateNamespace(ctx, ns)

	// Install with custom values (2 replicas, custom name)
	values := map[string]interface{}{
		"replicaCount": 2,
		"name":         "custom-app",
		"image":        "busybox:1.36",
	}

	if err := client.InstallHelmChart(ctx, ns, "override-test", chartPath, values); err != nil {
		t.Fatalf("InstallHelmChart: %v", err)
	}

	// Wait for pods
	time.Sleep(10 * time.Second)
	pods, _ := client.GetPods(ctx, ns, "app=custom-app")
	if len(pods) < 2 {
		t.Errorf("expected 2 pods (replicaCount override), got %d", len(pods))
	}

	// Verify deployment name matches override
	exists, _ := client.GetDeployment(ctx, ns, "custom-app")
	if !exists {
		t.Error("deployment 'custom-app' should exist (name override)")
	}

	// Cleanup
	client.UninstallHelmChart(ctx, ns, "override-test")
	client.DeleteNamespace(ctx, ns)
	t.Log("Helm value override test passed")
}

func TestK3sHelmInstallBadChart(t *testing.T) {
	ctx := context.Background()

	t.Log(testK3SStartMsg)
	container, err := k3s.Run(ctx, testK3SImage)
	if err != nil {
		t.Fatalf(testK3SStartFmt, err)
	}
	defer container.Terminate(ctx)

	kubeconfig, err := container.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf(testKubeconfigFmt, err)
	}

	kubeconfigFile := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kubeconfigFile, kubeconfig, 0600)
	t.Setenv("KUBECONFIG", kubeconfigFile)

	restCfg, _ := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	cs, _ := kubernetes.NewForConfig(restCfg)
	dynClient, _ := dynamic.NewForConfig(restCfg)

	client := &Client{
		clientset:     cs,
		dynamicClient: dynClient,
		restConfig:    restCfg,
	}

	// Install with non-existent chart path
	err = client.InstallHelmChart(ctx, "default", "bad-release", "/nonexistent/chart", nil)
	if err == nil {
		t.Error("expected error for non-existent chart path")
	}
	t.Logf("Got expected error: %v", err)
}
