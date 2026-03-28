package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Run with: go test ./internal/k8s/ -tags=integration -run TestIntegration -v -timeout 10m
// Requires: minikube running, CNPG operator installed, metrics-server installed

func TestIntegrationCRDApplyAndGet(t *testing.T) {
	client, err := NewClient()
	if err != nil {
		t.Skipf("K8s not available: %v", err)
	}
	ctx := context.Background()
	ns := "integration-test-" + time.Now().Format("150405")

	// Cleanup
	defer client.DeleteNamespace(ctx, ns)

	// Create namespace
	if err := client.CreateNamespace(ctx, ns); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}

	// Apply a CNPG cluster CRD
	cluster := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "integ-db",
		Namespace: ns,
		Tier:      config.TierConfig{Instances: 1, StorageSize: "1Gi", Memory: "256Mi", CPU: "0.25"},
	})

	if err := client.ApplyCRD(ctx, CNPGClusterGVR, ns, cluster); err != nil {
		t.Fatalf("ApplyCRD: %v", err)
	}

	// Get it back
	got, err := client.GetCRD(ctx, CNPGClusterGVR, ns, "integ-db-postgres")
	if err != nil {
		t.Fatalf("GetCRD: %v", err)
	}
	if got.GetName() != "integ-db-postgres" {
		t.Errorf("name: got %s", got.GetName())
	}

	// Wait for pod (with timeout)
	t.Log("Waiting for pod to be ready...")
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		ready, _ := client.IsPodReady(ctx, ns, "integ-db-postgres-1")
		if ready {
			t.Log("Pod is ready")
			break
		}
		time.Sleep(5 * time.Second)
	}

	// Test ExecInPod
	out, err := client.ExecInPod(ctx, ns, "integ-db-postgres-1", "postgres",
		[]string{"psql", "-U", "postgres", "-t", "-A", "-c", "SELECT version()"})
	if err != nil {
		t.Fatalf("ExecInPod: %v", err)
	}
	if out == "" {
		t.Error("exec output should not be empty")
	}
	t.Logf("PostgreSQL version: %s", out)

	// Test CNPG metrics from port 9187
	metrics, err := client.ExecInPod(ctx, ns, "integ-db-postgres-1", "postgres",
		[]string{"python3", "-c", "import urllib.request; print(urllib.request.urlopen('http://[::1]:9187/metrics').read().decode()[:200])"})
	if err != nil {
		t.Logf("WARN: metrics fetch failed (may need time): %v", err)
	} else {
		t.Logf("CNPG metrics sample: %s", metrics[:min(100, len(metrics))])
	}

	// Test GetSecret
	secret, err := client.GetSecret(ctx, ns, "integ-db-postgres-app")
	if err != nil {
		t.Logf("WARN: secret not ready yet: %v", err)
	} else {
		if string(secret["username"]) == "" {
			t.Error("username should not be empty")
		}
		t.Logf("Username: %s", secret["username"])
	}

	// Test GetPodMetrics (metrics-server)
	podMetrics, err := client.GetPodMetrics(ctx, ns)
	if err != nil {
		t.Logf("WARN: metrics-server not ready: %v", err)
	} else {
		for _, pm := range podMetrics {
			t.Logf("Pod %s: CPU=%dm, Memory=%dMi", pm.Name, pm.CPUMillis, pm.MemoryMB)
		}
	}

	// Cleanup: delete CRD and namespace
	client.DeleteCRD(ctx, CNPGClusterGVR, ns, "integ-db-postgres")
	// Namespace cleanup in defer
}

func TestIntegrationNamespaceLifecycle(t *testing.T) {
	client, err := NewClient()
	if err != nil {
		t.Skipf("K8s not available: %v", err)
	}
	ctx := context.Background()
	ns := "integ-ns-test"

	if err := client.CreateNamespace(ctx, ns); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Verify it exists via clientset
	_, err = client.clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace should exist: %v", err)
	}

	if err := client.DeleteNamespace(ctx, ns); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
