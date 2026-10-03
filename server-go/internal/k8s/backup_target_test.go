package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EXC-532 (owner decision 2026-10-02): every base backup runs on the
// primary. On a standby, pg_backup_start is cancelled by a recovery conflict
// under write load and the backup fails; one instance has only a primary.
func TestEveryBaseBackupRunsOnThePrimary(t *testing.T) {
	scheduled, err := BuildScheduledBackup("p1", "ns", "0 0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	first, err := BuildFirstScheduledBackup("p1", "ns", "0 0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	manual := BuildManualBackup("p1", "ns", "p1-backup-1")
	for name, backup := range map[string]*unstructured.Unstructured{"scheduled": scheduled, "first": first, "manual": manual} {
		if target, _, _ := unstructured.NestedString(backup.Object, "spec", "target"); target != "primary" {
			t.Errorf("%s backup target = %q, want primary", name, target)
		}
	}
}
