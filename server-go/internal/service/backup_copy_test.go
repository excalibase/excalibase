package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-667: a copy of now backs the source up and restores the copy from that
// backup; every copy needs the source's backups.

// copyAdapter is a backup adapter that can take a backup on demand.
type copyAdapter struct {
	plainAdapter
	configured bool
	backupID   string
	backupErr  error
	backups    int
	restored   []domain.RestoreRequest
	// phases are the restore phases reported when each call was made.
	phases []string
}

func (a *copyAdapter) BackupsConfigured() bool { return a.configured }

func (a *copyAdapter) TakeSafetyBackup(context.Context, *domain.DatabaseInstance) (string, error) {
	a.backups++
	return a.backupID, a.backupErr
}

func (a *copyAdapter) InPlaceRecoveryTarget(context.Context, *domain.DatabaseInstance, domain.RestoreRequest) (map[string]interface{}, error) {
	return nil, nil
}

func (a *copyAdapter) ReplaceDatabase(context.Context, *domain.DatabaseInstance, map[string]interface{}) error {
	return nil
}

func (a *copyAdapter) Restore(_ context.Context, _ *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	a.restored = append(a.restored, req)
	return &domain.ProvisioningResponse{ProjectID: req.TargetProjectID}, nil
}

// noCopyAdapter restores from backups but cannot take one on demand.
type noCopyAdapter struct {
	plainAdapter
}

func (noCopyAdapter) BackupsConfigured() bool { return true }

func copyService(t *testing.T, adapter BackupAdapter, edit func(*domain.DatabaseInstance)) (*BackupService, *domain.DatabaseInstance) {
	t.Helper()
	instances, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := &domain.DatabaseInstance{
		ProjectID: "src", OrgID: "org", DeploymentMode: domain.ModeK8s,
		Status: string(domain.StatusActive), BackupEnabled: boolPtr(true),
	}
	if edit != nil {
		edit(source)
	}
	if err := instances.Create(source); err != nil {
		t.Fatal(err)
	}
	svc := NewBackupServiceWithAdapters(instances, map[domain.DeploymentMode]BackupAdapter{domain.ModeK8s: adapter}, "")
	svc.SetOrgProjectCapacity(unlimitedCapacity{})
	return svc, source
}

func copyNow() domain.RestoreRequest {
	return domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "dst", CopyNow: true}
}

func TestCheckCopy_Refusals(t *testing.T) {
	latest := domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "dst"}
	cases := []struct {
		name    string
		adapter BackupAdapter
		edit    func(*domain.DatabaseInstance)
		req     domain.RestoreRequest
		want    error
	}{
		{"backups off", &copyAdapter{configured: true}, func(i *domain.DatabaseInstance) { i.BackupEnabled = boolPtr(false) }, copyNow(), ErrCopyNeedsBackups},
		{"backups never set", &copyAdapter{configured: true}, func(i *domain.DatabaseInstance) { i.BackupEnabled = nil }, latest, ErrCopyNeedsBackups},
		{"no backup store", &copyAdapter{}, nil, latest, ErrCopyNoBackupStore},
		{"now on an adapter that cannot back up on demand", noCopyAdapter{}, nil, copyNow(), ErrCopyNowUnsupported},
		{"now while paused", &copyAdapter{configured: true}, func(i *domain.DatabaseInstance) { i.Status = string(domain.StatusPaused) }, copyNow(), ErrCopyNowNeedsRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, source := copyService(t, tc.adapter, tc.edit)
			if err := svc.CheckCopy(source, tc.req); !errors.Is(err, tc.want) {
				t.Errorf("CheckCopy = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckCopy_Admits(t *testing.T) {
	paused := func(i *domain.DatabaseInstance) { i.Status = string(domain.StatusPaused) }
	cases := []struct {
		name    string
		adapter BackupAdapter
		edit    func(*domain.DatabaseInstance)
		req     domain.RestoreRequest
	}{
		{"now while running", &copyAdapter{configured: true}, nil, copyNow()},
		// The latest backup and a time are read from the store alone.
		{"latest while paused", &copyAdapter{configured: true}, paused, domain.RestoreRequest{NewProjectName: "c", TargetProjectID: "dst"}},
		{"latest on an adapter that cannot back up on demand", noCopyAdapter{}, nil, domain.RestoreRequest{NewProjectName: "c", TargetProjectID: "dst"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, source := copyService(t, tc.adapter, tc.edit)
			if err := svc.CheckCopy(source, tc.req); err != nil {
				t.Errorf("CheckCopy = %v", err)
			}
		})
	}
}

func TestRestoreSubmittedCopy_NowBacksUpThenRestoresFromThatBackup(t *testing.T) {
	adapter := &copyAdapter{configured: true, backupID: "backup-now"}
	svc, _ := copyService(t, adapter, nil)
	var phases []string
	ctx := context.WithValue(context.Background(), restoreProgressKey{}, func(phase string) { phases = append(phases, phase) })

	if err := svc.RestoreSubmittedCopy(ctx, "src", copyNow()); err != nil {
		t.Fatalf("RestoreSubmittedCopy: %v", err)
	}
	if adapter.backups != 1 {
		t.Fatalf("backups taken = %d, want 1", adapter.backups)
	}
	if len(adapter.restored) != 1 {
		t.Fatalf("restores = %d, want 1", len(adapter.restored))
	}
	got := adapter.restored[0]
	if got.BackupID != "backup-now" || got.CopyNow || got.TargetTime != nil || got.TargetProjectID != "dst" || got.NewProjectName != "copy" {
		t.Errorf("restored with %+v, want the copy pinned to backup-now", got)
	}
	if len(phases) == 0 || phases[0] != domain.RestoreStepSourceBackup {
		t.Errorf("phases = %v, want %s reported first", phases, domain.RestoreStepSourceBackup)
	}
}

func TestRestoreSubmittedCopy_NowStopsWhenTheBackupFails(t *testing.T) {
	adapter := &copyAdapter{configured: true, backupErr: errors.New("barman: s3://bucket/key?X-Amz-Signature=secret denied")}
	svc, _ := copyService(t, adapter, nil)

	err := svc.RestoreSubmittedCopy(context.Background(), "src", copyNow())
	if !errors.Is(err, ErrCopyBackupFailed) {
		t.Fatalf("RestoreSubmittedCopy = %v, want ErrCopyBackupFailed", err)
	}
	if len(adapter.restored) != 0 {
		t.Error("nothing may be restored from a backup that did not complete")
	}
	// The job shows only the public reason, never the store's detail.
	if reason := publicRestoreFailure(err); strings.Contains(reason, "secret") || reason != ErrCopyBackupFailed.Error() {
		t.Errorf("public failure = %q", reason)
	}
}

// The source may be paused or have backups turned off between submission and
// the job running; the job checks again before it backs anything up.
func TestRestoreSubmittedCopy_NowRechecksTheSource(t *testing.T) {
	adapter := &copyAdapter{configured: true, backupID: "b"}
	svc, _ := copyService(t, adapter, func(i *domain.DatabaseInstance) { i.Status = string(domain.StatusPaused) })

	err := svc.RestoreSubmittedCopy(context.Background(), "src", copyNow())
	if !errors.Is(err, ErrCopyNowNeedsRunning) {
		t.Fatalf("RestoreSubmittedCopy = %v, want ErrCopyNowNeedsRunning", err)
	}
	if adapter.backups != 0 || len(adapter.restored) != 0 {
		t.Error("a paused source must not be backed up or copied")
	}
	if reason := publicRestoreFailure(err); reason != ErrCopyNowNeedsRunning.Error() {
		t.Errorf("public failure = %q", reason)
	}
}

func TestRestoreSubmittedCopy_LatestTakesNoBackup(t *testing.T) {
	adapter := &copyAdapter{configured: true, backupID: "b"}
	svc, _ := copyService(t, adapter, nil)

	req := domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "dst"}
	if err := svc.RestoreSubmittedCopy(context.Background(), "src", req); err != nil {
		t.Fatalf("RestoreSubmittedCopy: %v", err)
	}
	if adapter.backups != 0 || len(adapter.restored) != 1 || adapter.restored[0].BackupID != "" {
		t.Errorf("backups=%d restores=%+v, want a plain restore", adapter.backups, adapter.restored)
	}
}
