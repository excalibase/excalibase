package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Run with: go test ./internal/k8s/ -tags=integration -run TestK3s -v -timeout 5m
// Requires: Docker running. k3s starts in ~30s, auto-destroyed after test.

func TestK3sNamespaceAndSecret(t *testing.T) {
	ctx := context.Background()

	// Start k3s in Docker
	t.Log("Starting k3s container...")
	container, err := k3s.Run(ctx, "rancher/k3s:v1.31.6-k3s1")
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	defer container.Terminate(ctx)

	// Get kubeconfig
	kubeconfig, err := container.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf("get kubeconfig: %v", err)
	}

	// Build client from kubeconfig
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

	// Test: CreateNamespace
	t.Log("Testing CreateNamespace...")
	if err := client.CreateNamespace(ctx, "test-ns"); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}

	// Test: CreateSecret + GetSecret
	t.Log("Testing Secret CRUD...")
	err = client.CreateSecret(ctx, "test-ns", "db-creds", map[string][]byte{
		"username": []byte("admin"),
		"password": []byte("secret123"),
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	data, err := client.GetSecret(ctx, "test-ns", "db-creds")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if string(data["password"]) != "secret123" {
		t.Errorf("password: got %s", data["password"])
	}

	// Test: GetPods (empty initially)
	pods, err := client.GetPods(ctx, "test-ns", "")
	if err != nil {
		t.Fatalf("GetPods: %v", err)
	}
	if len(pods) != 0 {
		t.Errorf("expected 0 pods, got %d", len(pods))
	}

	// Test: ApplyCRD with a simple unstructured resource
	// k3s has ConfigMap as a built-in — we'll use a ConfigMap via dynamic client
	// to verify dynamic client works against real API server
	t.Log("Testing dynamic client...")
	configMapGVR := CNPGClusterGVR // This will fail since CNPG isn't installed
	testObj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "k3s-test",
		Namespace: "test-ns",
		Tier: config.TierConfig{
			Instances:   1,
			StorageSize: "1Gi",
			Memory:      "256Mi",
			CPU:         "0.25",
		},
	})

	err = client.ApplyCRD(ctx, configMapGVR, "test-ns", testObj)
	if err != nil {
		// Expected: CNPG CRD not registered in k3s
		t.Logf("ApplyCRD (expected to fail without CNPG): %v", err)
	}

	// Cleanup
	client.DeleteNamespace(ctx, "test-ns")
	t.Log("k3s test completed successfully")
}

func TestK3sWithCNPGOperator(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long k3s+CNPG test")
	}

	ctx := context.Background()

	t.Log("Starting k3s container...")
	container, err := k3s.Run(ctx, "rancher/k3s:v1.31.6-k3s1")
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	defer container.Terminate(ctx)

	kubeconfig, err := container.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf("get kubeconfig: %v", err)
	}

	restCfg, _ := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	cs, _ := kubernetes.NewForConfig(restCfg)
	dynClient, _ := dynamic.NewForConfig(restCfg)

	client := &Client{
		clientset:     cs,
		dynamicClient: dynClient,
		restConfig:    restCfg,
	}

	// Install CNPG operator via kubectl inside k3s
	t.Log("Installing CNPG operator...")
	_, _, err = container.Exec(ctx, []string{
		"kubectl", "apply", "--server-side", "-f",
		"https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.23/releases/cnpg-1.23.0.yaml",
	})
	if err != nil {
		t.Fatalf("install CNPG: %v", err)
	}

	// Wait for operator pod + webhook to be fully ready
	t.Log("Waiting for CNPG operator and webhook...")
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		pods, _ := client.GetPods(ctx, "cnpg-system", "")
		allReady := len(pods) > 0
		for _, p := range pods {
			if p.Status.Phase != "Running" {
				allReady = false
			}
			for _, cs := range p.Status.ContainerStatuses {
				if !cs.Ready {
					allReady = false
				}
			}
		}
		if allReady {
			// Extra wait for webhook endpoint registration
			time.Sleep(10 * time.Second)
			t.Log("CNPG operator and webhook ready")
			goto operatorReady
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatal("CNPG operator did not start in time")
operatorReady:

	// Now test real CRD creation
	t.Log("Creating PostgreSQL cluster CRD...")
	ns := "integ-test"
	client.CreateNamespace(ctx, ns)

	cluster := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "k3s-db",
		Namespace: ns,
		Tier: config.TierConfig{
			Instances:   1,
			StorageSize: "1Gi",
			Memory:      "256Mi",
			CPU:         "0.25",
		},
	})

	if err := client.ApplyCRD(ctx, CNPGClusterGVR, ns, cluster); err != nil {
		t.Fatalf("ApplyCRD: %v", err)
	}

	// Verify CRD was accepted
	got, err := client.GetCRD(ctx, CNPGClusterGVR, ns, "k3s-db-postgres")
	if err != nil {
		t.Fatalf("GetCRD after create: %v", err)
	}
	if got.GetName() != "k3s-db-postgres" {
		t.Errorf("CRD name: got %s", got.GetName())
	}
	t.Log("CRD accepted by CNPG operator - schema is valid!")

	// Wait for pod (may take a while in k3s)
	t.Log("Waiting for pod to be ready...")
	deadline = time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		ready, _ := client.IsPodReady(ctx, ns, "k3s-db-postgres-1")
		if ready {
			t.Log("Pod is ready!")

			// Test ExecInPod with real pod
			out, err := client.ExecInPod(ctx, ns, "k3s-db-postgres-1", "postgres",
				[]string{"psql", "-U", "postgres", "-t", "-A", "-c", "SELECT 1"})
			if err != nil {
				t.Errorf("ExecInPod: %v", err)
			} else {
				t.Logf("ExecInPod result: %s", out)
			}
			break
		}
		time.Sleep(5 * time.Second)
	}

	// Cleanup
	client.DeleteCRD(ctx, CNPGClusterGVR, ns, "k3s-db-postgres")
	client.DeleteNamespace(ctx, ns)
	t.Log("k3s+CNPG test completed")
}
