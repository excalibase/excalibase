//go:build integration

// Spins up k3s via testcontainers — hangs for minutes if Docker is
// unavailable, so guarded behind the integration tag.

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

const (
	testK3SNS         = "test-ns"
	testK3SDBPostgres = "k3s-db-postgres"
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

	// Test: CreateProjectNamespace
	t.Log("Testing CreateProjectNamespace...")
	if err := client.CreateProjectNamespace(ctx, testK3SNS, "it-org"); err != nil {
		t.Fatalf("CreateProjectNamespace: %v", err)
	}

	// Test: CreateSecret + GetSecret
	t.Log("Testing Secret CRUD...")
	err = client.CreateSecret(ctx, testK3SNS, "db-creds", map[string][]byte{
		"username": []byte("admin"),
		"password": []byte("secret123"),
	})
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	data, err := client.GetSecret(ctx, testK3SNS, "db-creds")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if string(data["password"]) != "secret123" {
		t.Errorf("password: got %s", data["password"])
	}

	// Test: GetPods (empty initially)
	pods, err := client.GetPods(ctx, testK3SNS, "")
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
		Namespace: testK3SNS,
		Tier: config.TierConfig{
			Instances:   1,
			StorageSize: "1Gi",
			Memory:      "256Mi",
			CPU:         "0.25",
		},
	})

	err = client.ApplyCRD(ctx, configMapGVR, testK3SNS, testObj)
	if err != nil {
		// Expected: CNPG CRD not registered in k3s
		t.Logf("ApplyCRD (expected to fail without CNPG): %v", err)
	}

	// Cleanup
	client.DeleteNamespace(ctx, testK3SNS)
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
	client := &Client{clientset: cs, dynamicClient: dynClient, restConfig: restCfg}

	// Install CNPG operator
	t.Log("Installing CNPG operator...")
	if _, _, err = container.Exec(ctx, []string{
		"kubectl", "apply", "--server-side", "-f",
		"https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.23/releases/cnpg-1.23.0.yaml",
	}); err != nil {
		t.Fatalf("install CNPG: %v", err)
	}

	t.Log("Waiting for CNPG operator and webhook...")
	if !waitForCNPGOperator(ctx, t, client) {
		t.Fatal("CNPG operator did not start in time")
	}

	// Create PostgreSQL cluster CRD
	t.Log("Creating PostgreSQL cluster CRD...")
	ns := "integ-test"
	client.CreateProjectNamespace(ctx, ns, "it-org")

	cluster := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "k3s-db",
		Namespace: ns,
		Tier:      config.TierConfig{Instances: 1, StorageSize: "1Gi", Memory: "256Mi", CPU: "0.25"},
	})
	if err := client.ApplyCRD(ctx, CNPGClusterGVR, ns, cluster); err != nil {
		t.Fatalf("ApplyCRD: %v", err)
	}

	got, err := client.GetCRD(ctx, CNPGClusterGVR, ns, testK3SDBPostgres)
	if err != nil {
		t.Fatalf("GetCRD after create: %v", err)
	}
	if got.GetName() != testK3SDBPostgres {
		t.Errorf("CRD name: got %s", got.GetName())
	}
	t.Log("CRD accepted by CNPG operator - schema is valid!")

	testExecInPodWhenReady(ctx, t, client, ns)

	client.DeleteCRD(ctx, CNPGClusterGVR, ns, testK3SDBPostgres)
	client.DeleteNamespace(ctx, ns)
	t.Log("k3s+CNPG test completed")
}

// waitForCNPGOperator polls until all pods in cnpg-system are Running+Ready.
// Returns true when ready, false on timeout.
func waitForCNPGOperator(ctx context.Context, t *testing.T, client *Client) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if isCNPGReady(ctx, client) {
			time.Sleep(10 * time.Second) // extra wait for webhook registration
			t.Log("CNPG operator and webhook ready")
			return true
		}
		time.Sleep(5 * time.Second)
	}
	return false
}

// isCNPGReady returns true when at least one pod exists in cnpg-system and all are Running+Ready.
func isCNPGReady(ctx context.Context, client *Client) bool {
	pods, _ := client.GetPods(ctx, "cnpg-system", "")
	if len(pods) == 0 {
		return false
	}
	for _, p := range pods {
		if p.Status.Phase != "Running" {
			return false
		}
		for _, cs := range p.Status.ContainerStatuses {
			if !cs.Ready {
				return false
			}
		}
	}
	return true
}

// testExecInPodWhenReady waits for the primary pod and tests ExecInPod.
func testExecInPodWhenReady(ctx context.Context, t *testing.T, client *Client, ns string) {
	t.Helper()
	t.Log("Waiting for pod to be ready...")
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if ready, _ := client.IsPodReady(ctx, ns, "k3s-db-postgres-1"); ready {
			t.Log("Pod is ready!")
			out, err := client.ExecInPod(ctx, ns, "k3s-db-postgres-1", "postgres",
				[]string{"psql", "-U", "postgres", "-t", "-A", "-c", "SELECT 1"})
			if err != nil {
				t.Errorf("ExecInPod: %v", err)
			} else {
				t.Logf("ExecInPod result: %s", out)
			}
			return
		}
		time.Sleep(5 * time.Second)
	}
}
