package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	backupTypeManual       = "MANUAL"
	backupTypeScheduled    = "SCHEDULED"
	backupStatusInProgress = "IN_PROGRESS"
	// cnpgScheduledBackupLabel marks a Backup the operator created for a
	// ScheduledBackup.
	cnpgScheduledBackupLabel = "cnpg.io/scheduled-backup"
	cnpgBackupCompleted      = "completed"
)

// ErrRestoreBackupUnusable refuses a restore from a backup the source project
// does not have as a completed recovery point.
var ErrRestoreBackupUnusable = errors.New("restore: the backup is not a completed backup of this project")

// backupStatusOf maps the operator's phase to the API's. A backup whose WAL
// archiving fails, or whose definition the operator rejected, will never
// complete, so it has failed.
func backupStatusOf(phase string) string {
	switch phase {
	case cnpgBackupCompleted:
		return backupStatusCompleted
	case "failed", "walArchivingFailing", "invalid backup definition":
		return backupStatusFailed
	default:
		return backupStatusInProgress
	}
}

// backupRefOf describes one operator Backup. The project id is the caller's,
// never read from the object.
func backupRefOf(projectID string, obj *unstructured.Unstructured) BackupRef {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	started, _, _ := unstructured.NestedString(obj.Object, "status", "startedAt")
	if started == "" {
		started = obj.GetCreationTimestamp().UTC().Format("2006-01-02T15:04:05Z")
	}
	stopped, _, _ := unstructured.NestedString(obj.Object, "status", "stoppedAt")
	reason, _, _ := unstructured.NestedString(obj.Object, "status", "error")
	kind := backupTypeManual
	if _, scheduled := obj.GetLabels()[cnpgScheduledBackupLabel]; scheduled {
		kind = backupTypeScheduled
	}
	return BackupRef{
		ID:         obj.GetName(),
		ProjectID:  projectID,
		Type:       kind,
		Status:     backupStatusOf(phase),
		StartedAt:  started,
		FinishedAt: stopped,
		Error:      reason,
	}
}

// recoveryTarget is the recoveryTarget a restore renders. A backup id names
// one of the source's completed Backups; recovery starts from it and, unless
// a later target is also asked for, stops as soon as it is consistent — the
// state the backup captured, not whatever the WAL archive holds after it.
func (a *K8sBackupAdapter) recoveryTarget(ctx context.Context, src *domain.DatabaseInstance, req domain.RestoreRequest) (map[string]interface{}, error) {
	target := req.RecoveryTarget()
	if req.BackupID == "" {
		return target, nil
	}
	barmanID, err := a.completedBackupID(ctx, src, req.BackupID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		target = map[string]interface{}{"targetImmediate": true}
	}
	target["backupID"] = barmanID
	return target, nil
}

// completedBackupID is the id the object store knows a completed Backup of
// the source's cluster by.
func (a *K8sBackupAdapter) completedBackupID(ctx context.Context, src *domain.DatabaseInstance, name string) (string, error) {
	obj, err := a.k8sClient.GetCRD(ctx, k8s.CNPGBackupGVR, src.Namespace, name)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrRestoreBackupUnusable, name, err)
	}
	cluster, _, _ := unstructured.NestedString(obj.Object, "spec", "cluster", "name")
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	barmanID, _, _ := unstructured.NestedString(obj.Object, "status", "backupId")
	switch {
	case cluster != src.ProjectID+postgresClusterSuffix:
		return "", fmt.Errorf("%w: %s belongs to another cluster", ErrRestoreBackupUnusable, name)
	case phase != cnpgBackupCompleted:
		return "", fmt.Errorf("%w: %s is %q", ErrRestoreBackupUnusable, name, phase)
	case barmanID == "":
		return "", fmt.Errorf("%w: %s has no backup id", ErrRestoreBackupUnusable, name)
	}
	return barmanID, nil
}
