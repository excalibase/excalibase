package k8s

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EXC-329: each tier's statement_timeout must reach the CNPG postgres params so
// a runaway query is cancelled rather than pegging a shared box.
func TestCluster_StatementTimeoutFromTier(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "proj-x", Namespace: "ns-x",
		Tier: config.TierConfig{CPU: "0.5", Memory: "512Mi", StorageSize: "5Gi", Instances: 1, StatementTimeout: "15s"},
	})
	spec := obj.Object["spec"].(map[string]interface{})
	params := spec["postgresql"].(map[string]interface{})["parameters"].(map[string]interface{})
	if params["statement_timeout"] != "15s" {
		t.Errorf("statement_timeout = %v, want 15s", params["statement_timeout"])
	}
	if params["idle_in_transaction_session_timeout"] != "15s" {
		t.Errorf("idle_in_transaction_session_timeout = %v, want 15s", params["idle_in_transaction_session_timeout"])
	}
}

// An empty StatementTimeout leaves the param unset (unbounded — back-compat).
func TestCluster_NoStatementTimeoutWhenEmpty(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "proj-y", Namespace: "ns-y",
		Tier: config.TierConfig{CPU: "0.5", Memory: "512Mi", StorageSize: "5Gi", Instances: 1},
	})
	spec := obj.Object["spec"].(map[string]interface{})
	params := spec["postgresql"].(map[string]interface{})["parameters"].(map[string]interface{})
	if _, ok := params["statement_timeout"]; ok {
		t.Error("statement_timeout must be unset when the tier defines none")
	}
}

// EXC-329: creating a project namespace applies a count-based ResourceQuota so a
// tenant cannot balloon the shared box with unbounded pods/PVCs.
func TestCreateNamespaceWithLabels_AppliesQuota(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	ns := "org1-proj-quota"
	if err := c.CreateNamespaceWithLabels(ctx, ns, map[string]string{"x": "y"}); err != nil {
		t.Fatalf("CreateNamespaceWithLabels: %v", err)
	}
	q, err := c.clientset.CoreV1().ResourceQuotas(ns).Get(ctx, "namespace-quota", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("quota not created: %v", err)
	}
	pods := q.Spec.Hard["pods"]
	if pods.Value() != 20 {
		t.Errorf("pods quota = %d, want 20", pods.Value())
	}
	if _, ok := q.Spec.Hard["persistentvolumeclaims"]; !ok {
		t.Error("quota must cap persistentvolumeclaims")
	}
	// Must NOT gate on cpu/memory requests — that would reject CNPG's
	// no-request maintenance pods.
	if _, ok := q.Spec.Hard["requests.cpu"]; ok {
		t.Error("quota must be count-based, not a cpu-request quota (would break CNPG jobs)")
	}
}
