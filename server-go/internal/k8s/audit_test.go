package k8s

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A database with the pgaudit extension needs the library preloaded to run any
// DDL, so every route that rebuilds its cluster has to keep the setting (EXC-688).
func TestBuildRestoreClusterKeepsTheAuditSetting(t *testing.T) {
	cluster := clusterOpts("dst", "org-dst")
	cluster.AuditLog = "ddl,write"
	got := AuditLog(mustBuildRestore(t, restoreOf(cluster)))
	if got != "ddl,write" {
		t.Errorf("restored cluster pgaudit.log = %q, want ddl,write", got)
	}
	if AuditLog(mustBuildRestore(t, restoreOf(clusterOpts("dst", "org-dst")))) != "" {
		t.Error("a source without audit gained it")
	}
}

func TestWithSizingOfKeepsTheClustersOwnAuditSetting(t *testing.T) {
	before := freeCluster(t)
	moved, _ := WithPostgresParameter(WithTier(before, config.TierConfig{Instances: 3, StorageSize: "50Gi", Memory: "4Gi", CPU: "2", StatementTimeout: "30s"}, "50Gi"), AuditLogParameter, "ddl")
	back := WithSizingOf(moved, before)
	if got := AuditLog(back); got != "ddl" {
		t.Errorf("after a rollback pgaudit.log = %q, want ddl kept", got)
	}
	if specString(t, back, "postgresql", "parameters", "statement_timeout") != "15s" {
		t.Error("the previous settings were not put back")
	}
}

func TestWithSizingOfDoesNotInventAuditOrKeepAStaleOne(t *testing.T) {
	withAudit, _ := WithPostgresParameter(freeCluster(t), AuditLogParameter, "all")
	plain := freeCluster(t)
	if AuditLog(WithSizingOf(plain, plain)) != "" {
		t.Error("audit appeared from nowhere")
	}
	// previous had a different audit value than the cluster now has: the cluster's wins
	older, _ := WithPostgresParameter(freeCluster(t), AuditLogParameter, "read")
	if got := AuditLog(WithSizingOf(withAudit, older)); got != "all" {
		t.Errorf("pgaudit.log = %q, want the cluster's own all", got)
	}
}

func TestAuditLogReadsTheClusterSetting(t *testing.T) {
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{}}
	if AuditLog(cluster) != "" {
		t.Error("an empty cluster has audit")
	}
}
