package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// seedDockerBackups files a completed base backup of dk-1 for each start time.
func seedDockerBackups(t *testing.T, uploader *fakeS3Uploader, records *fakeBackupRecordStore, starts map[string]time.Time) {
	t.Helper()
	for id, at := range starts {
		uploader.objects["test-backups/backups/dk-1/manual/"+id+".tar.gz"] = minimalGzippedTar(t)
		_ = records.Save(context.Background(), &domain.BackupRecord{ID: id, ProjectID: "dk-1", Status: "COMPLETED",
			Type: "MANUAL", Timestamp: at.UTC().Format(time.RFC3339)})
	}
}

func restoreDockerCopy(t *testing.T, plans *fakeRestorePlans, target *time.Time) (*fakeRegistrar, *fakeDockerClientForAdapter, error) {
	t.Helper()
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	adapter.SetInstanceStore(store)
	adapter.SetRestorePlanSource(plans)
	dc := &fakeDockerClientForAdapter{}
	adapter.SetDockerClient(dc)
	registrar := &fakeRegistrar{store: store}
	adapter.SetProjectRegistrar(registrar)
	now := time.Now()
	seedDockerBackups(t, uploader, records, map[string]time.Time{
		"backup-old": now.Add(-3 * time.Hour), "backup-mid": now.Add(-2 * time.Hour), "backup-new": now.Add(-30 * time.Minute),
	})
	src, _ := store.FindByProjectID("dk-1")
	req := domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "copy"}
	if target != nil {
		req.TargetTime = &domain.ZonedTime{Time: *target}
	}
	_, err := adapter.Restore(context.Background(), src, req)
	return registrar, dc, err
}

// EXC-593: a copy is a new project, backed up by its plan like any other,
// and archiving its WAL from the start.
func TestDockerRestoreGivesTheCopyItsPlansBackups(t *testing.T) {
	registrar, dc, err := restoreDockerCopy(t, backedUpEnterprisePlan(), nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	copy := registrar.calls[0]
	if !backupsOn(copy) || copy.BackupSchedule != testBackupSchedule || *copy.BackupRetentionDays != 14 {
		t.Fatalf("copy backups %v %q", copy.BackupEnabled, copy.BackupSchedule)
	}
	archived := false
	for _, cmd := range dc.execs {
		archived = archived || strings.Contains(strings.Join(cmd, " "), "archive_command")
	}
	if !archived || !slices.Contains(dc.copyDsts, "/") {
		t.Fatalf("the copy does not archive: copies %v", dc.copyDsts)
	}
}

// A point in time is recovered from the last backup taken before it; a later
// one cannot be rolled back.
func TestDockerRestoreRecoversFromTheLastBackupBeforeThePoint(t *testing.T) {
	point := time.Now().Add(-90 * time.Minute)
	registrar, _, err := restoreDockerCopy(t, enterprisePlan(), &point)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := registrar.calls[0].RestoredFromBackupID; got != "backup-mid" {
		t.Fatalf("recovered from %s", got)
	}
}

func TestDockerRestoreRefusesAPointBeforeEveryBackup(t *testing.T) {
	point := time.Now().Add(-5 * time.Hour)
	_, dc, err := restoreDockerCopy(t, enterprisePlan(), &point)
	var target *RestoreTargetError
	if err == nil || !errors.As(err, &target) || dc.createdName != "" {
		t.Fatalf("err = %v, container %q", err, dc.createdName)
	}
}
