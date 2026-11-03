package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EXC-668: after a major upgrade a restore runs the major its restore point
// was written on and reads that major's archive.

type upgradedK8sWorld struct {
	svc     *BackupService
	mock    *k8s.MockClient
	store   *storage.FileSystemStore
	guard   *recordingGuard
	stopped time.Time
}

func newUpgradedK8sWorld(t *testing.T) upgradedK8sWorld {
	t.Helper()
	config.PublishPostgresCatalogForTest()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	svc := NewBackupService(store, mock, dir, StaticBackupStorage(r2Storage()))
	svc.SetProjectRegistrar(&fakeRegistrar{store: store})
	armRestore(svc, mock, store)
	guard := &recordingGuard{}
	svc.adapters[domain.ModeK8s].(*K8sBackupAdapter).SetRestoreTargetGuard(guard)
	_ = store.Create(&domain.DatabaseInstance{
		ProjectID: "src", OrgID: "org", Namespace: "org-src", Status: "ACTIVE",
		DBType: domain.PostgreSQL, PostgresVersion: "17", BackupEnabled: boolPtr(true),
	})
	runs := newMemMajorUpgrades(store)
	converted := time.Now().Add(-time.Hour)
	stopped := converted.Add(-10 * time.Minute)
	runs.runs["mu-1"] = &domain.MajorUpgrade{ID: "mu-1", ProjectID: "src", FromVersion: "16", ToVersion: "17",
		Status: domain.MajorUpgradeCompleted, Converted: true, WritesStoppedAt: &stopped, ConvertedAt: &converted}
	svc.SetMajorUpgradeArchives(NewMajorUpgradeArchives(runs))
	for name, major := range map[string]int64{"before": 16, "after": 17} {
		backup := cnpgBackup(name, "src-postgres", "completed", "barman-"+name)
		_ = unstructured.SetNestedField(backup.Object, major, "status", "majorVersion")
		mock.CRDs["org-src/"+name] = backup
	}
	return upgradedK8sWorld{svc: svc, mock: mock, store: store, guard: guard, stopped: stopped}
}

func (w upgradedK8sWorld) restoredFrom(t *testing.T) (image, destination, server string) {
	t.Helper()
	cluster := w.mock.CRDs["org-dst/dst-postgres"]
	if cluster == nil {
		t.Fatal("no restored cluster")
	}
	image, _, _ = unstructured.NestedString(cluster.Object, "spec", "imageName")
	external, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "externalClusters")
	server, _, _ = unstructured.NestedString(external[0].(map[string]interface{}), "plugin", "parameters", "serverName")
	source := w.mock.CRDs["org-dst/"+k8s.RecoverySourceObjectStoreName("dst")]
	destination, _, _ = unstructured.NestedString(source.Object, "spec", "configuration", "destinationPath")
	return image, destination, server
}

func copyToDst(req domain.RestoreRequest) domain.RestoreRequest {
	req.NewProjectName, req.TargetProjectID = "dst", "dst"
	return req
}

func mustImage(t *testing.T, major string) string {
	t.Helper()
	image, err := config.PostgresImage(major)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func TestK8sCopyOfTheLatestReadsTheUpgradedArchive(t *testing.T) {
	w := newUpgradedK8sWorld(t)
	if _, err := w.svc.RestoreFromBackup(context.Background(), "src", copyToDst(domain.RestoreRequest{})); err != nil {
		t.Fatal(err)
	}
	image, destination, server := w.restoredFrom(t)
	if image != mustImage(t, "17") || destination != "s3://excalibase-backups/src/cloud" || server != "pg17" {
		t.Fatalf("image %q destination %q server %q", image, destination, server)
	}
}

func TestK8sCopyOfABackupFromBeforeTheUpgradeRunsTheOldMajor(t *testing.T) {
	w := newUpgradedK8sWorld(t)
	if _, err := w.svc.RestoreFromBackup(context.Background(), "src", copyToDst(domain.RestoreRequest{BackupID: "before"})); err != nil {
		t.Fatal(err)
	}
	image, destination, server := w.restoredFrom(t)
	if image != mustImage(t, "16") || destination != "s3://excalibase-backups/src" || server != "cloud" {
		t.Fatalf("image %q destination %q server %q", image, destination, server)
	}
}

func TestK8sCopyToATimeBeforeTheUpgradeReadsTheFinishedOldArchive(t *testing.T) {
	w := newUpgradedK8sWorld(t)
	at := &domain.ZonedTime{Time: w.stopped.Add(-time.Hour)}
	if _, err := w.svc.RestoreFromBackup(context.Background(), "src", copyToDst(domain.RestoreRequest{TargetTime: at})); err != nil {
		t.Fatal(err)
	}
	image, _, server := w.restoredFrom(t)
	if image != mustImage(t, "16") || server != "cloud" {
		t.Fatalf("image %q server %q", image, server)
	}
	if len(w.guard.targets) != 0 {
		t.Fatal("the running database was asked to archive WAL for an archive it no longer writes")
	}
}

func TestInPlaceRestoreToAPointBeforeTheUpgradeIsRefused(t *testing.T) {
	w := newUpgradedK8sWorld(t)
	w.svc.SetInPlaceRestore(&InPlaceRestore{})
	for _, req := range []domain.RestoreRequest{
		{BackupID: "before"},
		{TargetTime: &domain.ZonedTime{Time: w.stopped.Add(-time.Hour)}},
	} {
		if err := w.svc.CheckInPlaceRestorePoint(context.Background(), "src", req); !errors.Is(err, ErrRestorePointBeforeMajorUpgrade) {
			t.Fatalf("CheckInPlaceRestorePoint(%+v) = %v", req, err)
		}
		if err := w.svc.RestoreInPlace(context.Background(), "src", req); !errors.Is(err, ErrRestorePointBeforeMajorUpgrade) {
			t.Fatalf("RestoreInPlace(%+v) = %v", req, err)
		}
	}
	if err := w.svc.CheckInPlaceRestorePoint(context.Background(), "src", domain.RestoreRequest{BackupID: "after"}); err != nil {
		t.Fatalf("a backup on the current major was refused: %v", err)
	}
}

func TestBackupMajorOfACNPGBackup(t *testing.T) {
	w := newUpgradedK8sWorld(t)
	adapter := w.svc.adapters[domain.ModeK8s].(*K8sBackupAdapter)
	inst, _ := w.store.FindByProjectID("src")
	if major, err := adapter.BackupMajor(context.Background(), inst, "before"); err != nil || major != "16" {
		t.Fatalf("major = %q, %v", major, err)
	}
	unsaid := cnpgBackup("old", "src-postgres", "completed", "barman-old")
	w.mock.CRDs["org-src/old"] = unsaid
	if _, err := adapter.BackupMajor(context.Background(), inst, "old"); !errors.Is(err, ErrRestoreBackupUnusable) {
		t.Fatalf("a backup that says nothing was given a major: %v", err)
	}
	for name, when := range map[string]time.Time{"16": w.stopped.Add(-time.Hour), "17": time.Now()} {
		_ = unstructured.SetNestedField(unsaid.Object, when.UTC().Format(time.RFC3339), "status", "startedAt")
		if major, err := adapter.BackupMajor(context.Background(), inst, "old"); err != nil || major != name {
			t.Fatalf("a backup started %s without a major = %q, %v; want %s", when, major, err, name)
		}
	}
	if _, err := adapter.BackupMajor(context.Background(), inst, "missing"); err == nil {
		t.Fatal("a missing backup has a major")
	}
}

func TestBackupMajorOfADockerBackup(t *testing.T) {
	adapter, inst, uploader, records, _ := upgradedDockerAdapter(t)
	seedGenerations(t, uploader, records)
	if major, err := adapter.BackupMajor(context.Background(), inst, "pre-new"); err != nil || major != "16" {
		t.Fatalf("major = %q, %v", major, err)
	}
	if major, _ := adapter.BackupMajor(context.Background(), inst, "pre-old"); major != "" {
		t.Fatalf("an unsaid record gave %q", major)
	}
	if _, err := adapter.BackupMajor(context.Background(), inst, "missing"); err == nil {
		t.Fatal("a missing record has a major")
	}
}
