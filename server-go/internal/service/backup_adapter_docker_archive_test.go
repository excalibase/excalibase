package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-668: a single host's project moved from 16 to 17 writes its backups to
// archive pg17; what it wrote on 16 stays where it was, untouched.

func upgradedDockerAdapter(t *testing.T) (*DockerBackupAdapter, *domain.DatabaseInstance, *fakeS3Uploader, *fakeBackupRecordStore, *storage.FileSystemStore) {
	t.Helper()
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	runs := newMemMajorUpgrades(store)
	converted := time.Now().Add(-24 * time.Hour)
	stopped := converted.Add(-time.Minute)
	runs.runs["mu-1"] = &domain.MajorUpgrade{ID: "mu-1", ProjectID: "dk-1", FromVersion: "16", ToVersion: "17",
		Status: domain.MajorUpgradeCompleted, Converted: true, ConvertedAt: &converted, WritesStoppedAt: &stopped}
	adapter.SetMajorUpgradeArchives(NewMajorUpgradeArchives(runs))
	inst, _ := store.FindByProjectID("dk-1")
	return adapter, inst, uploader, records, store
}

func TestDockerAdapterShipsWALOfAnUpgradedDatabaseToItsOwnArchive(t *testing.T) {
	adapter, inst, uploader, _, _ := upgradedDockerAdapter(t)
	adapter.SetDockerClient(&fakeDockerClientForAdapter{walArchive: map[string][]byte{segment: []byte("wal")}})
	if err := adapter.ShipWAL(context.Background(), inst); err != nil {
		t.Fatalf("ShipWAL: %v", err)
	}
	if _, ok := uploader.objects["test-backups/backups/dk-1/pg17/wals/"+segment+".gz"]; !ok {
		t.Fatalf("objects = %v", keysOf(uploader.objects))
	}
	if _, ok := uploader.objects["test-backups/backups/dk-1/wals/"+segment+".gz"]; ok {
		t.Fatal("the upgraded database wrote into the archive of the one it replaced")
	}
}

func TestDockerBackupRecordsTheMajorItWasTakenOn(t *testing.T) {
	adapter, inst, _, records, _ := upgradedDockerAdapter(t)
	adapter.SetDockerClient(&fakeDockerClientForAdapter{walName: segment})
	ref, err := adapter.TriggerManual(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := records.ListByProject(context.Background(), "dk-1")
	for _, record := range saved {
		if record.ID == ref.ID && record.PostgresVersion != "17" {
			t.Fatalf("record version = %q", record.PostgresVersion)
		}
	}
}

func seedGenerations(t *testing.T, uploader *fakeS3Uploader, records *fakeBackupRecordStore) {
	t.Helper()
	now := time.Now()
	for id, seed := range map[string]struct {
		version string
		at      time.Time
	}{
		"pre-old": {"", now.Add(-40 * 24 * time.Hour)}, "pre-new": {"16", now.Add(-30 * 24 * time.Hour)},
		"post-old": {"17", now.Add(-20 * 24 * time.Hour)}, "post-new": {"17", now.Add(-2 * 24 * time.Hour)},
	} {
		uploader.objects["test-backups/backups/dk-1/manual/"+id+".tar.gz"] = minimalGzippedTar(t)
		_ = records.Save(context.Background(), &domain.BackupRecord{ID: id, ProjectID: "dk-1", Status: "COMPLETED",
			Type: "MANUAL", Timestamp: seed.at.UTC().Format(time.RFC3339), PostgresVersion: seed.version})
	}
}

func TestDockerRestorePointsOnlyReachTheirOwnMajorsBackups(t *testing.T) {
	adapter, inst, uploader, records, _ := upgradedDockerAdapter(t)
	seedGenerations(t, uploader, records)
	latest, err := adapter.resolveSourceBackup(context.Background(), inst, domain.RestoreRequest{})
	if err != nil || latest.ID != "post-new" {
		t.Fatalf("latest on 17 = %+v, %v", latest, err)
	}
	old := *inst
	old.PostgresVersion = "16"
	latestOld, err := adapter.resolveSourceBackup(context.Background(), &old, domain.RestoreRequest{})
	if err != nil || latestOld.ID != "pre-new" {
		t.Fatalf("latest on 16 = %+v, %v", latestOld, err)
	}
	early := &domain.ZonedTime{Time: time.Now().Add(-35 * 24 * time.Hour)}
	if _, err := adapter.resolveSourceBackup(context.Background(), inst, domain.RestoreRequest{TargetTime: early}); err == nil {
		t.Fatal("a time before the upgrade resolved to a backup on 17")
	}
	if _, err := adapter.resolveSourceBackup(context.Background(), inst, domain.RestoreRequest{BackupID: "pre-old"}); err == nil {
		t.Fatal("a backup on 16 resolved for a restore on 17")
	}
}

func TestDockerRetentionLeavesTheArchiveOfAnEarlierMajorWhole(t *testing.T) {
	adapter, inst, uploader, records, _ := upgradedDockerAdapter(t)
	seedGenerations(t, uploader, records)
	for _, id := range []string{"post-old", "post-new"} {
		uploader.objects["test-backups/backups/dk-1/manual/"+id+".wal"] = []byte("000000010000000000000005")
	}
	for _, name := range []string{"000000010000000000000003", "000000010000000000000009"} {
		uploader.objects["test-backups/backups/dk-1/wals/"+name+".gz"] = []byte("old")
		uploader.objects["test-backups/backups/dk-1/pg17/wals/"+name+".gz"] = []byte("new")
	}
	seven := 7
	inst.BackupRetentionDays = &seven
	if _, err := adapter.PurgeExpired(context.Background(), inst); err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	for key, kept := range map[string]bool{
		"manual/pre-old.tar.gz": true, "manual/pre-new.tar.gz": true,
		"manual/post-old.tar.gz": true, "manual/post-new.tar.gz": true,
		"wals/000000010000000000000003.gz": true, "wals/000000010000000000000009.gz": true,
		"pg17/wals/000000010000000000000003.gz": false, "pg17/wals/000000010000000000000009.gz": true,
	} {
		if _, ok := uploader.objects["test-backups/backups/dk-1/"+key]; ok != kept {
			t.Errorf("%s kept=%v, want %v", key, ok, kept)
		}
	}
	left, _ := records.ListByProject(context.Background(), "dk-1")
	if len(left) != 4 {
		t.Fatalf("records left %d, want 4", len(left))
	}
}

func TestDockerRestoreOfAnEarlierMajorRunsItsImageOnItsArchive(t *testing.T) {
	adapter, inst, uploader, records, store := upgradedDockerAdapter(t)
	adapter.SetInstanceStore(store)
	dc := &fakeDockerClientForAdapter{}
	adapter.SetDockerClient(dc)
	adapter.SetProjectRegistrar(&fakeRegistrar{store: store})
	seedGenerations(t, uploader, records)
	uploader.objects["test-backups/backups/dk-1/wals/000000010000000000000004.gz"] = gzipped("old wal")
	old := *inst
	old.PostgresVersion = "16"
	if _, err := adapter.Restore(context.Background(), &old, domain.RestoreRequest{NewProjectName: "back", TargetProjectID: "back"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if dc.createdImg != "postgres:16" {
		t.Fatalf("image = %q", dc.createdImg)
	}
	if dc.copyCalls < 2 {
		t.Fatalf("the old archive's WAL was not copied in (copies %d)", dc.copyCalls)
	}
}

func keysOf(objects map[string][]byte) []string {
	keys := make([]string, 0, len(objects))
	for key := range objects {
		keys = append(keys, strings.TrimPrefix(key, "test-backups/"))
	}
	return keys
}
