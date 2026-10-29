package service

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// MajorUpgradeArchives says which backup archive each major of a project
// writes to and which one a restore point reads (EXC-668). The project's
// converted upgrade runs are the only record of it: the major it was created
// on keeps the original archive, every later one has "pg<major>".
//
// A nil *MajorUpgradeArchives is a platform without major upgrades: every
// project is on its original archive.
type MajorUpgradeArchives struct {
	runs storage.MajorUpgradeStore
}

func NewMajorUpgradeArchives(runs storage.MajorUpgradeStore) *MajorUpgradeArchives {
	return &MajorUpgradeArchives{runs: runs}
}

// BackupMajorReader names the major a project's backup was taken on; "" when
// the backup does not say, which is the project's original major.
type BackupMajorReader func(ctx context.Context, inst *domain.DatabaseInstance, backupID string) (string, error)

func (a *MajorUpgradeArchives) converted(ctx context.Context, projectID string) ([]domain.MajorUpgrade, error) {
	if a == nil || a.runs == nil {
		return nil, nil
	}
	runs, err := a.runs.ConvertedMajorUpgrades(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("read the major upgrades of %s: %w", projectID, err)
	}
	return runs, nil
}

// OriginalMajor is the major the project's database was created on.
func (a *MajorUpgradeArchives) OriginalMajor(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	runs, err := a.converted(ctx, inst.ProjectID)
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return inst.PostgresVersion, nil
	}
	return runs[0].FromVersion, nil
}

// Archive names where backups of inst's project taken on major live.
func (a *MajorUpgradeArchives) Archive(ctx context.Context, inst *domain.DatabaseInstance, major string) (string, error) {
	original, err := a.OriginalMajor(ctx, inst)
	if err != nil {
		return "", err
	}
	return domain.BackupArchiveName(original, major), nil
}

// RestorePoint is the project as a restore of req must see it: on the major
// the restore point was written on, so the restore runs that major's image
// and reads its archive. A time between the old database stopping and the
// conversion is the end of the old archive, so the target time is dropped.
// inst is not modified; without an upgrade it is returned as is.
func (a *MajorUpgradeArchives) RestorePoint(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest,
	backupMajor BackupMajorReader) (*domain.DatabaseInstance, domain.RestoreRequest, error) {
	runs, err := a.converted(ctx, inst.ProjectID)
	if err != nil || len(runs) == 0 {
		return inst, req, err
	}
	major, adjusted, err := restorePointMajor(ctx, inst, runs, req, backupMajor)
	if err != nil {
		return nil, req, err
	}
	if major == inst.PostgresVersion {
		return inst, adjusted, nil
	}
	view := *inst
	view.PostgresVersion = major
	return &view, adjusted, nil
}

// MajorAt is the major inst's data was on at instant at: the from-version of
// the first conversion after it, else the current one.
func (a *MajorUpgradeArchives) MajorAt(ctx context.Context, inst *domain.DatabaseInstance, at time.Time) (string, error) {
	runs, err := a.converted(ctx, inst.ProjectID)
	if err != nil {
		return "", err
	}
	for _, run := range runs {
		if run.ConvertedAt != nil && at.Before(*run.ConvertedAt) {
			return run.FromVersion, nil
		}
	}
	return inst.PostgresVersion, nil
}

func restorePointMajor(ctx context.Context, inst *domain.DatabaseInstance, runs []domain.MajorUpgrade,
	req domain.RestoreRequest, backupMajor BackupMajorReader) (string, domain.RestoreRequest, error) {
	switch {
	case req.BackupID != "":
		if backupMajor == nil {
			return inst.PostgresVersion, req, nil
		}
		major, err := backupMajor(ctx, inst, req.BackupID)
		if err != nil {
			return "", req, err
		}
		if major == "" {
			major = runs[0].FromVersion
		}
		return major, req, nil
	case req.TargetTime != nil:
		return majorAtTime(inst, runs, req)
	}
	return inst.PostgresVersion, req, nil
}

func majorAtTime(inst *domain.DatabaseInstance, runs []domain.MajorUpgrade, req domain.RestoreRequest) (string, domain.RestoreRequest, error) {
	point := req.TargetTime.Time
	for _, run := range runs {
		if run.ConvertedAt == nil || !point.Before(*run.ConvertedAt) {
			continue
		}
		if run.WritesStoppedAt != nil && !point.Before(*run.WritesStoppedAt) {
			req.TargetTime = nil
		}
		return run.FromVersion, req, nil
	}
	return inst.PostgresVersion, req, nil
}
