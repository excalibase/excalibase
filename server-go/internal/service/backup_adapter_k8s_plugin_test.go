package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const sourceBackupName = "src-backup-20260926-223600"

func metav1Time(value string) metav1.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return metav1.NewTime(parsed)
}

// cnpgBackup is a Backup as the operator leaves it in the source namespace.
func cnpgBackup(name, cluster, phase, barmanID string) *unstructured.Unstructured {
	obj := k8s.BuildManualBackup("src", "org-src", name)
	_ = unstructured.SetNestedField(obj.Object, cluster, "spec", "cluster", "name")
	obj.SetCreationTimestamp(metav1Time("2026-09-26T22:36:00Z"))
	status := map[string]interface{}{"phase": phase}
	if barmanID != "" {
		status["backupId"] = barmanID
	}
	_ = unstructured.SetNestedMap(obj.Object, status, "status")
	return obj
}

func withSourceBackup(mock *k8s.MockClient, backup *unstructured.Unstructured) {
	mock.CRDs["org-src/"+backup.GetName()] = backup
}

func recoveryTargetOf(t *testing.T, mock *k8s.MockClient) map[string]interface{} {
	t.Helper()
	target, _, _ := unstructured.NestedMap(mock.CRDs["org-dst/dst-postgres"].Object, "spec", "bootstrap", "recovery", "recoveryTarget")
	return target
}

func TestK8sRestoreCreatesItsStoresBeforeTheCluster(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetRestorePlanSource(backedUpEnterprisePlan())

	if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	cluster := slices.Index(mock.Calls, "ApplyCRD:org-dst/dst-postgres")
	for _, store := range []string{k8s.RecoverySourceObjectStoreName("dst"), k8s.BackupObjectStoreName("dst")} {
		at := slices.Index(mock.Calls, "ApplyCRD:org-dst/"+store)
		if at < 0 || at > cluster {
			t.Errorf("%s must exist before the cluster that uses it: %v", store, mock.Calls)
		}
	}
	own := mock.CRDs["org-dst/"+k8s.BackupObjectStoreName("dst")]
	if retention, _, _ := unstructured.NestedString(own.Object, "spec", "retentionPolicy"); retention != "14d" {
		t.Errorf("the restored project keeps its plan's retention, got %q", retention)
	}
}

func TestK8sRestoreWithoutBackupsCreatesNoStoreOfItsOwn(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})

	if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, ok := mock.CRDs["org-dst/"+k8s.BackupObjectStoreName("dst")]; ok {
		t.Error("a restored project without backups must not get a store to archive to")
	}
}

func TestK8sRestoreByBackupIDStopsAtThatBackup(t *testing.T) {
	mock := k8s.NewMockClient()
	withSourceBackup(mock, cnpgBackup(sourceBackupName, "src-postgres", "completed", "20260926T223600"))
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})

	if _, err := adapter.Restore(context.Background(), sourceInstance(),
		domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst", BackupID: sourceBackupName}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	target := recoveryTargetOf(t, mock)
	if target["backupID"] != "20260926T223600" || target["targetImmediate"] != true || len(target) != 2 {
		t.Errorf("a backup restore must start from that backup and stop once consistent, got %v", target)
	}
}

func TestK8sRestoreByBackupIDAndTimeReplaysToTheTime(t *testing.T) {
	mock := k8s.NewMockClient()
	withSourceBackup(mock, cnpgBackup(sourceBackupName, "src-postgres", "completed", "20260926T223600"))
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetRestoreTargetGuard(&recordingGuard{})
	at := &domain.ZonedTime{Time: time.Date(2026, 9, 26, 22, 40, 0, 0, time.UTC)}

	if _, err := adapter.Restore(context.Background(), sourceInstance(),
		domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst", BackupID: sourceBackupName, TargetTime: at}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	target := recoveryTargetOf(t, mock)
	if target["backupID"] != "20260926T223600" || target["targetTime"] != "2026-09-26T22:40:00.000000Z" || len(target) != 2 {
		t.Errorf("got %v", target)
	}
}

func TestK8sRestoreRefusesABackupItCannotUse(t *testing.T) {
	cases := map[string]*unstructured.Unstructured{
		"no such backup":            nil,
		"another cluster's backup":  cnpgBackup(sourceBackupName, "other-postgres", "completed", "20260926T223600"),
		"a backup still running":    cnpgBackup(sourceBackupName, "src-postgres", "running", ""),
		"a failed backup":           cnpgBackup(sourceBackupName, "src-postgres", "failed", ""),
		"a completed backup, no id": cnpgBackup(sourceBackupName, "src-postgres", "completed", ""),
	}
	for name, backup := range cases {
		t.Run(name, func(t *testing.T) {
			mock := k8s.NewMockClient()
			if backup != nil {
				withSourceBackup(mock, backup)
			}
			adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
			_, err := adapter.Restore(context.Background(), sourceInstance(),
				domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst", BackupID: sourceBackupName})
			if !errors.Is(err, ErrRestoreBackupUnusable) {
				t.Fatalf("err: got %v, want ErrRestoreBackupUnusable", err)
			}
			if len(mock.Namespaces) != 0 || len(mock.Secrets) != 0 {
				t.Errorf("nothing may be created: ns=%v secrets=%v", mock.Namespaces, mock.Secrets)
			}
		})
	}
}

func TestK8sBackupListReportsWhatTheOperatorReports(t *testing.T) {
	mock := k8s.NewMockClient()
	inst := sourceInstance()
	failing := cnpgBackup("src-b2", "src-postgres", "walArchivingFailing", "")
	_ = unstructured.SetNestedField(failing.Object, "WAL archiving is failing", "status", "error")
	scheduled := cnpgBackup("src-postgres-backup-20260927020000", "src-postgres", "completed", "20260927T020000")
	scheduled.SetLabels(map[string]string{"cnpg.io/scheduled-backup": "src-postgres-backup"})
	_ = unstructured.SetNestedField(scheduled.Object, "2026-09-27T02:00:05Z", "status", "stoppedAt")
	for _, backup := range []*unstructured.Unstructured{
		cnpgBackup("src-b1", "src-postgres", "running", ""), failing, scheduled,
		cnpgBackup("other-b1", "other-postgres", "completed", "x"),
	} {
		withSourceBackup(mock, backup)
	}
	adapter := NewK8sBackupAdapter(mock, StaticBackupStorage(r2Storage()))

	refs, err := adapter.List(context.Background(), inst)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]BackupRef{}
	for _, ref := range refs {
		got[ref.ID] = ref
	}
	if len(got) != 3 {
		t.Fatalf("only this project's backups are listed, got %v", refs)
	}
	if got["src-b1"].Status != "IN_PROGRESS" || got["src-b1"].Type != "MANUAL" {
		t.Errorf("running: %+v", got["src-b1"])
	}
	if got["src-b2"].Status != "FAILED" || got["src-b2"].Error != "WAL archiving is failing" {
		t.Errorf("a backup whose WAL archiving fails has failed: %+v", got["src-b2"])
	}
	done := got["src-postgres-backup-20260927020000"]
	if done.Status != "COMPLETED" || done.Type != "SCHEDULED" || done.FinishedAt != "2026-09-27T02:00:05Z" || done.ProjectID != "src" {
		t.Errorf("scheduled: %+v", done)
	}
}

func TestK8sTriggerTakesAPluginBackupTheListSees(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := NewK8sBackupAdapter(mock, StaticBackupStorage(r2Storage()))
	ref, err := adapter.TriggerManual(context.Background(), sourceInstance())
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	method, _, _ := unstructured.NestedString(mock.CRDs["org-src/"+ref.ID].Object, "spec", "method")
	if method != "plugin" {
		t.Errorf("the backup must be taken by the plugin, got method %q", method)
	}
	refs, _ := adapter.List(context.Background(), sourceInstance())
	if len(refs) != 1 || refs[0].ID != ref.ID || refs[0].Status != "IN_PROGRESS" {
		t.Errorf("list: %+v", refs)
	}
}
