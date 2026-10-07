package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Once wired, an in-place restore reaches the cluster instead of being refused
// as unconfigured.
func TestWireInPlaceRestoreConfiguresTheBackupService(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backupSvc := service.NewBackupService(store, k8s.NewMockClient(), t.TempDir(), nil)
	wireInPlaceRestore(backupSvc, service.NewProvisioningService(store, nil, nil), store, nil)
	err = backupSvc.RestoreInPlace(context.Background(), "missing", domain.RestoreRequest{})
	if err == nil || errors.Is(err, service.ErrInPlaceRestoreNotConfigured) {
		t.Fatalf("got %v", err)
	}
}

// An in-place job is run as an in-place restore, never as a copy.
func TestRestoreStepRunsAnInPlaceJobInPlace(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "proj-a", OrgID: "o", DeploymentMode: domain.ModeK8s, Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	backupSvc := service.NewBackupService(store, k8s.NewMockClient(), t.TempDir(), nil)
	job := &domain.RestoreJob{
		SourceProjectID: "proj-a", NewProjectID: "proj-a", Mode: domain.RestoreModeInPlace,
		Request: domain.RestoreRequest{Mode: domain.RestoreModeInPlace, ConfirmReplace: true, TargetProjectID: "proj-a"},
	}
	if err := runRestoreStep(context.Background(), store, backupSvc, job); !errors.Is(err, service.ErrInPlaceRestoreNotConfigured) {
		t.Fatalf("an in-place job must reach the in-place restore, got %v", err)
	}
}

func TestRestoreStepRequestIsTheSubmittedOne(t *testing.T) {
	at := &domain.ZonedTime{Time: time.Date(2026, 9, 26, 22, 40, 0, 0, time.UTC)}
	job := &domain.RestoreJob{
		NewProjectID: "proj-new", NewProjectName: "copy", TargetKind: "time",
		Request: domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "proj-new", BackupID: "b1", TargetTime: at},
	}
	req, err := restoreStepRequest(job)
	if err != nil {
		t.Fatalf("restoreStepRequest: %v", err)
	}
	if req.BackupID != "b1" || req.TargetTime != at || req.TargetProjectID != "proj-new" || req.NewProjectName != "copy" {
		t.Errorf("got %+v", req)
	}
}

func TestRestoreStepRefusesAJobWithoutItsRequest(t *testing.T) {
	cases := map[string]*domain.RestoreJob{
		"no request":         {NewProjectID: "proj-new", NewProjectName: "copy", TargetKind: "latest"},
		"another project id": {NewProjectID: "proj-new", Request: domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "proj-other"}},
	}
	for name, job := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := restoreStepRequest(job); err == nil {
				t.Error("a restore must not run on a request rebuilt or guessed from the job row")
			}
		})
	}
}
