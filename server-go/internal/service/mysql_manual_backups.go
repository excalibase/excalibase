package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A manual MySQL backup is a Job from MOCO's CronJob, which that CronJob's
// history limit deletes after the next scheduled run. So each manual run is
// also recorded: IN_PROGRESS with the time it was asked for, then pinned to
// its run's time (COMPLETED) or to FAILED once its Job is seen finished.

// manualRunStartWindow bounds how long after being asked for a manual run
// whose Job was never seen finished can have started.
const manualRunStartWindow = time.Hour

func (a *MySQLBackupAdapter) recordManualRun(ctx context.Context, projectID, jobName string, asked time.Time) error {
	if a.records == nil {
		return errors.New("manual MySQL backups need the backup record store")
	}
	err := a.records.Save(ctx, &domain.BackupRecord{
		ID: jobName, ProjectID: projectID, Timestamp: asked.UTC().Format(time.RFC3339),
		Type: backupTypeManual, Status: backupStatusInProgress,
	})
	if err != nil {
		return fmt.Errorf("record the manual backup: %w", err)
	}
	return nil
}

// manualRuns are the IDs of the completed runs started by hand.
func (a *MySQLBackupAdapter) manualRuns(ctx context.Context, projectID string, completed []mysqlBackup, jobs []*unstructured.Unstructured) (map[string]bool, error) {
	records, err := a.manualRecords(ctx, projectID)
	if err != nil {
		return nil, err
	}
	manual := map[string]bool{}
	live := map[string]*unstructured.Unstructured{}
	for _, job := range jobs {
		live[job.GetName()] = job
		if _, recorded := records[job.GetName()]; recorded || !k8s.IsManualMySQLBackupJob(job) {
			continue
		}
		if state := k8s.MySQLBackupJobState(job); state.Phase == k8s.MySQLJobSucceeded {
			if run, ok := firstRunBetween(completed, state.Started, state.Finished); ok {
				manual[run.ID] = true
			}
		}
	}
	for _, record := range records {
		run, ok, err := a.manualRunOf(ctx, record, live[record.ID], completed)
		if err != nil {
			return nil, err
		}
		if ok {
			manual[run.ID] = true
		}
	}
	return manual, nil
}

func (a *MySQLBackupAdapter) manualRecords(ctx context.Context, projectID string) (map[string]domain.BackupRecord, error) {
	byJob := map[string]domain.BackupRecord{}
	if a.records == nil {
		return byJob, nil
	}
	records, err := a.records.ListByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list the manual backups of %s: %w", projectID, err)
	}
	for _, record := range records {
		if record.ProjectID == projectID && record.Type == backupTypeManual {
			byJob[record.ID] = record
		}
	}
	return byJob, nil
}

// manualRunOf is the run a manual record names, pinning the record when its
// Job is seen finished.
func (a *MySQLBackupAdapter) manualRunOf(ctx context.Context, record domain.BackupRecord, job *unstructured.Unstructured, completed []mysqlBackup) (mysqlBackup, bool, error) {
	at, err := time.Parse(time.RFC3339, record.Timestamp)
	if err != nil {
		return mysqlBackup{}, false, fmt.Errorf("manual backup record %s has no time: %w", record.ID, err)
	}
	switch record.Status {
	case backupStatusFailed:
		return mysqlBackup{}, false, nil
	case backupStatusCompleted:
		run, ok := firstRunBetween(completed, at, at)
		return run, ok, nil
	}
	if job == nil {
		run, ok := firstRunBetween(completed, at, at.Add(manualRunStartWindow))
		return run, ok, nil
	}
	state := k8s.MySQLBackupJobState(job)
	switch state.Phase {
	case k8s.MySQLJobFailed:
		return mysqlBackup{}, false, a.pin(ctx, record, backupStatusFailed, at)
	case k8s.MySQLJobSucceeded:
		run, ok := firstRunBetween(completed, at, state.Finished)
		if !ok {
			return mysqlBackup{}, false, nil
		}
		return run, true, a.pin(ctx, record, backupStatusCompleted, run.At)
	}
	return mysqlBackup{}, false, nil
}

func (a *MySQLBackupAdapter) pin(ctx context.Context, record domain.BackupRecord, status string, at time.Time) error {
	record.Status, record.Timestamp = status, at.UTC().Format(time.RFC3339)
	if err := a.records.Save(ctx, &record); err != nil {
		return fmt.Errorf("record the outcome of manual backup %s: %w", record.ID, err)
	}
	return nil
}

// firstRunBetween is the oldest completed run from..until, to the second as
// MOCO names runs; a zero until is no bound.
func firstRunBetween(completed []mysqlBackup, from, until time.Time) (mysqlBackup, bool) {
	from = from.UTC().Truncate(time.Second)
	for _, run := range completed {
		if run.At.Before(from) {
			continue
		}
		if !until.IsZero() && run.At.After(until) {
			return mysqlBackup{}, false
		}
		return run, true
	}
	return mysqlBackup{}, false
}
