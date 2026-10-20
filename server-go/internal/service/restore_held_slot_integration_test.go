//go:build integration

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
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// EXC-639 on the real platform database, which counts a running copy as
// holding its slot: with the source and that copy filling a two-project plan,
// the copy's own restore runs, and any other copy is refused.
//
// Run: go test -tags=integration ./internal/service/ -run TestARunningCopyRunsOnItsOwnSlot -v
func TestARunningCopyRunsOnItsOwnSlot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	platform := startPlatformPostgres(ctx, t)
	source := &domain.DatabaseInstance{
		ProjectID: "proj-heldsrc", OrgID: "org-held", Tier: domain.Free, StorageSize: "5Gi",
		DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
	}
	if err := platform.Create(source); err != nil {
		t.Fatalf("register source: %v", err)
	}
	job := &domain.RestoreJob{
		ID: "restore-held", SourceProjectID: source.ProjectID, NewProjectID: "proj-heldcopy",
		Mode: domain.RestoreModeNewProject, Status: domain.RestoreStatusRunning,
		TargetKind: "latest", Owner: "replica-a",
	}
	if err := pgstore.NewRestoreJobs(platform).StartRestoreJob(ctx, job, storage.ProjectSlot{OrgID: "org-held", MaxProjects: 2}); err != nil {
		t.Fatalf("start the copy: %v", err)
	}

	provisioning := NewProvisioningService(platform, nil, k8s.NewMockClient())
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org-held", domain.Free)
	provisioning.SetOrgStore(orgs)
	free, err := config.GetTierConfig(domain.Free)
	if err != nil {
		t.Fatal(err)
	}
	free.MaxProjects = 2
	if _, err := platform.UpsertTierConfig(ctx, domain.Free, free, nil); err != nil {
		t.Fatalf("widen FREE: %v", err)
	}
	provisioning.SetTierStore(platform)
	adapter := &fakeAdapter{}
	backups := NewBackupServiceWithAdapters(platform, map[domain.DeploymentMode]BackupAdapter{domain.ModeK8s: adapter}, "")
	backups.SetOrgProjectCapacity(provisioning)

	req := domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: job.NewProjectID}
	if err := backups.RestoreSubmittedCopy(ctx, source.ProjectID, req); err != nil {
		t.Fatalf("the running copy was refused: %v", err)
	}
	other := domain.RestoreRequest{NewProjectName: "other", TargetProjectID: "proj-heldother"}
	var limit *OrgProjectLimitError
	if _, err := backups.RestoreFromBackup(ctx, source.ProjectID, other); !errors.As(err, &limit) {
		t.Fatalf("a third project = %v, want the project limit", err)
	}
	if adapter.restoreCalls != 1 {
		t.Fatalf("restore calls = %d, want 1", adapter.restoreCalls)
	}
}
