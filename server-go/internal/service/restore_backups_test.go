package service

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestARestoredProjectIsBackedUpFromTheMomentItExists(t *testing.T) {
	mock := k8s.NewMockClient()
	store := emptyInstanceStore(t)
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{store: store})
	adapter.SetInstanceStore(store)
	adapter.SetRestorePlanSource(backedUpEnterprisePlan())
	backups := NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{domain.ModeK8s: adapter}, t.TempDir())

	if _, err := restoreDst(t, adapter, tenantSource()); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	configured, err := backups.BackupsConfigured("dst")
	if err != nil || !configured {
		t.Errorf("the restored project must accept backups: configured=%v err=%v", configured, err)
	}
	destination, _, _ := unstructured.NestedString(restoredCluster(t, mock).Object, "spec", "backup", "barmanObjectStore", "destinationPath")
	if !strings.HasSuffix(destination, "/dst") {
		t.Errorf("the restored project must archive to its own prefix, got %q", destination)
	}
	scheduled := mock.CRDs["org-dst/dst-postgres-backup"]
	if scheduled == nil {
		t.Fatalf("no ScheduledBackup for the restored project: %v", mock.CRDs)
	}
	if immediate, _, _ := unstructured.NestedBool(scheduled.Object, "spec", "immediate"); !immediate {
		t.Error("the restored project must take a base backup of its own now, not at the next schedule")
	}
}
