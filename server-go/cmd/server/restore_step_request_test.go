package main

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestRestoreStepRequestIsTheSubmittedOne(t *testing.T) {
	at := &domain.FlexTime{Time: time.Date(2026, 9, 26, 22, 40, 0, 0, time.UTC)}
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
