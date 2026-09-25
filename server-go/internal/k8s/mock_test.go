package k8s

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
)

const (
	testMockDelNS  = "del-ns"
	testPostgresNS = "t-postgres"
)

func TestMockClientDeleteNamespace(t *testing.T) {
	m := NewMockClient()
	m.Namespaces[testMockDelNS] = true
	m.DeleteNamespace(context.Background(), testMockDelNS)
	if m.Namespaces[testMockDelNS] {
		t.Error("namespace not deleted")
	}
}

func TestMockClientSecrets(t *testing.T) {
	m := NewMockClient()
	m.CreateSecret(context.Background(), "ns", "sec", map[string][]byte{"key": []byte("val")})

	data, err := m.GetSecret(context.Background(), "ns", "sec")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if string(data["key"]) != "val" {
		t.Errorf("key: got %s", data["key"])
	}
}

func TestMockClientSecretNotFound(t *testing.T) {
	m := NewMockClient()
	_, err := m.GetSecret(context.Background(), "ns", "nope")
	if err == nil {
		t.Error("expected error for missing secret")
	}
}

func TestMockClientExecInPod(t *testing.T) {
	m := NewMockClient()
	m.ExecOutput["ns/pod"] = "hello world"

	out, err := m.ExecInPod(context.Background(), "ns", "pod", "c", []string{"echo"})
	if err != nil {
		t.Fatalf("ExecInPod: %v", err)
	}
	if out != "hello world" {
		t.Errorf("got %s", out)
	}
}

func TestMockClientExecError(t *testing.T) {
	m := NewMockClient()
	m.ExecError["ns/pod"] = context.DeadlineExceeded

	_, err := m.ExecInPod(context.Background(), "ns", "pod", "c", nil)
	if err == nil {
		t.Error("expected error")
	}
}

func TestMockClientIsPodReady(t *testing.T) {
	m := NewMockClient()
	m.PodReady["ns/pod-1"] = true

	ready, err := m.IsPodReady(context.Background(), "ns", "pod-1")
	if err != nil || !ready {
		t.Error("pod should be ready")
	}

	ready, err = m.IsPodReady(context.Background(), "ns", "pod-2")
	if err == nil {
		t.Error("expected error for unknown pod")
	}
}

func TestMockClientCRDLifecycle(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "t", Namespace: "ns",
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})

	m.ApplyCRD(ctx, CNPGClusterGVR, "ns", obj)

	got, err := m.GetCRD(ctx, CNPGClusterGVR, "ns", testPostgresNS)
	if err != nil || got == nil {
		t.Error("CRD should exist")
	}

	m.DeleteCRD(ctx, CNPGClusterGVR, "ns", testPostgresNS)
	_, err = m.GetCRD(ctx, CNPGClusterGVR, "ns", testPostgresNS)
	if err == nil {
		t.Error("CRD should be deleted")
	}
}

func TestMockClientSetupPostgreSQLMock(t *testing.T) {
	m := NewMockClient()
	m.SetupPostgreSQLMock("db", "ns", 3)

	// 3 pods ready
	for i := 1; i <= 3; i++ {
		ready, _ := m.IsPodReady(context.Background(), "ns", "db-postgres-"+string(rune('0'+i)))
		if !ready {
			t.Errorf("pod %d not ready", i)
		}
	}

	// Secret exists
	_, err := m.GetSecret(context.Background(), "ns", "db-postgres-app")
	if err != nil {
		t.Error("secret should exist")
	}

	// Metrics output available
	out, _ := m.ExecInPod(context.Background(), "ns", "db-postgres-1", "postgres", nil)
	if out == "" {
		t.Error("metrics output should be set")
	}

	// Metrics-server data
	podM, err := m.GetPodMetrics(context.Background(), "ns")
	if err != nil || len(podM) != 3 {
		t.Errorf("expected 3 pod metrics, got %d", len(podM))
	}
}

func TestBuildManualBackup(t *testing.T) {
	obj := BuildManualBackup("db", "ns", "db-backup-20260326")
	meta := obj.Object["metadata"].(map[string]interface{})
	if meta["name"] != "db-backup-20260326" {
		t.Errorf("name: got %v", meta["name"])
	}
	spec := obj.Object["spec"].(map[string]interface{})
	cluster := spec["cluster"].(map[string]interface{})
	if cluster["name"] != "db-postgres" {
		t.Errorf("cluster: got %v", cluster["name"])
	}
}

func TestMockClientGetPods(t *testing.T) {
	m := NewMockClient()
	m.SetupPostgreSQLMock("db", "ns", 2)
	pods, err := m.GetPods(context.Background(), "ns", "")
	if err != nil {
		t.Fatalf("GetPods: %v", err)
	}
	if len(pods) != 2 {
		t.Errorf("expected 2 pods, got %d", len(pods))
	}
}

func TestMockClientGetPodMetricsNotFound(t *testing.T) {
	m := NewMockClient()
	_, err := m.GetPodMetrics(context.Background(), "empty-ns")
	if err == nil {
		t.Error("expected error for missing metrics")
	}
}

func TestMockClientCallTracking(t *testing.T) {
	m := NewMockClient()
	m.CreateProjectNamespace(context.Background(), "ns", "org")
	m.DeleteNamespace(context.Background(), "ns")

	if len(m.Calls) != 2 {
		t.Errorf("expected 2 calls, got %d", len(m.Calls))
	}
}

func TestMockClientUpdateCRD(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "t", Namespace: "ns",
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})

	if err := m.UpdateCRD(ctx, CNPGClusterGVR, "ns", obj); err == nil {
		t.Error("UpdateCRD on a missing object must fail")
	}
	_ = m.ApplyCRD(ctx, CNPGClusterGVR, "ns", obj)
	obj.SetAnnotations(map[string]string{"k": "v"})
	if err := m.UpdateCRD(ctx, CNPGClusterGVR, "ns", obj); err != nil {
		t.Fatalf("UpdateCRD: %v", err)
	}
	got, _ := m.GetCRD(ctx, CNPGClusterGVR, "ns", testPostgresNS)
	if got.GetAnnotations()["k"] != "v" {
		t.Error("UpdateCRD should replace the stored object")
	}
	if m.Calls[len(m.Calls)-2] != "UpdateCRD:ns/"+testPostgresNS {
		t.Errorf("UpdateCRD call not recorded: %v", m.Calls)
	}
}

func TestMockClientAutoReconcileStampsClusterStatus(t *testing.T) {
	m := NewMockClient()
	m.AutoReconcileClusters = true
	cluster := BuildRestoreCluster(RestoreClusterOpts{
		SourceProjectID: "src", NewProjectID: "dst", Namespace: "ns",
		Store: ObjectStoreOpts{Bucket: "b"},
	})

	if err := m.ApplyCRD(context.Background(), CNPGClusterGVR, "ns", cluster); err != nil {
		t.Fatalf("ApplyCRD: %v", err)
	}

	obj, err := m.GetCRD(context.Background(), CNPGClusterGVR, "ns", "dst-postgres")
	if err != nil {
		t.Fatalf("GetCRD: %v", err)
	}
	primary, _, _ := unstructured.NestedString(obj.Object, "status", "currentPrimary")
	ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyInstances")
	if primary != "dst-postgres-1" || ready != 1 {
		t.Errorf("reconciled status: primary=%q ready=%d", primary, ready)
	}
}

func TestMockClientPodReadyErrorIsReported(t *testing.T) {
	m := NewMockClient()
	m.WildcardPodReady = true
	m.PodReadyError = errors.New("apiserver unreachable")

	if _, err := m.IsPodReady(context.Background(), "ns", "pod-1"); err == nil {
		t.Fatal("a configured readiness error must surface")
	}
}
