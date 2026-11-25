package k8s

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// HibernatedBackupFailure is what CNPG says about a Backup it refused because
// the cluster was hibernated.
const HibernatedBackupFailure = "cannot backup a hibernated cluster"

// suspendFlipAttempts bounds the read-modify-write retries on a lost race.
const suspendFlipAttempts = 5

// ScheduledBackupName is the name of a project's nightly schedule.
func ScheduledBackupName(projectID string) string { return projectID + "-postgres-backup" }

// SetScheduledBackupSuspended turns a project's nightly schedule off or on. A
// paused project's cluster is hibernated and CNPG fails every backup of it, so
// the schedule is suspended for the pause. A project without a schedule has
// nothing to flip.
func SetScheduledBackupSuspended(ctx context.Context, client KubeClient, namespace, projectID string, suspend bool) error {
	var err error
	for attempt := 0; attempt < suspendFlipAttempts; attempt++ {
		if err = flipScheduledBackup(ctx, client, namespace, projectID, suspend); !apierrors.IsConflict(err) {
			return err
		}
	}
	return err
}

func flipScheduledBackup(ctx context.Context, client KubeClient, namespace, projectID string, suspend bool) error {
	schedules, err := client.ListCRDs(ctx, CNPGScheduledBackupGVR, namespace)
	if err != nil {
		return fmt.Errorf("list scheduled backups: %w", err)
	}
	name := ScheduledBackupName(projectID)
	for _, schedule := range schedules {
		if schedule.GetName() != name {
			continue
		}
		if current, _, _ := unstructured.NestedBool(schedule.Object, "spec", "suspend"); current == suspend {
			return nil
		}
		flipped := schedule.DeepCopy()
		if err := unstructured.SetNestedField(flipped.Object, suspend, "spec", "suspend"); err != nil {
			return err
		}
		if err := client.UpdateCRD(ctx, CNPGScheduledBackupGVR, namespace, flipped); err != nil {
			return fmt.Errorf("set scheduled backup suspend=%v: %w", suspend, err)
		}
		return nil
	}
	return nil
}

// ScheduledBackupSuspended reports whether the project's schedule is suspended.
// A project with no schedule reports false.
func ScheduledBackupSuspended(ctx context.Context, client KubeClient, namespace, projectID string) (bool, error) {
	schedules, err := client.ListCRDs(ctx, CNPGScheduledBackupGVR, namespace)
	if err != nil {
		return false, fmt.Errorf("list scheduled backups: %w", err)
	}
	for _, schedule := range schedules {
		if schedule.GetName() == ScheduledBackupName(projectID) {
			suspended, _, _ := unstructured.NestedBool(schedule.Object, "spec", "suspend")
			return suspended, nil
		}
	}
	return false, nil
}

// ClusterHibernated reports whether the project's CNPG cluster carries
// cnpg.io/hibernation=on. A cluster that cannot be read is an error: guessing
// either way would suspend or start backups on no evidence.
func ClusterHibernated(ctx context.Context, client KubeClient, namespace, projectID string) (bool, error) {
	cluster, err := client.GetCRD(ctx, CNPGClusterGVR, namespace, projectID+"-postgres")
	if err != nil {
		return false, fmt.Errorf("get cluster for hibernation state: %w", err)
	}
	return cluster.GetAnnotations()["cnpg.io/hibernation"] == "on", nil
}

// DeleteHibernatedBackupFailures removes the Backups that failed only because
// the cluster was hibernated. They hold no data. A Backup that failed for any
// other reason stays: it is the evidence of a real problem.
func DeleteHibernatedBackupFailures(ctx context.Context, client KubeClient, namespace string) (int, error) {
	backups, err := client.ListCRDs(ctx, CNPGBackupGVR, namespace)
	if err != nil {
		return 0, fmt.Errorf("list backups: %w", err)
	}
	removed := 0
	for _, backup := range backups {
		if !failedBecauseHibernated(backup) {
			continue
		}
		if err := client.DeleteCRD(ctx, CNPGBackupGVR, namespace, backup.GetName()); err != nil {
			return removed, fmt.Errorf("delete backup %s: %w", backup.GetName(), err)
		}
		removed++
	}
	return removed, nil
}

func failedBecauseHibernated(backup *unstructured.Unstructured) bool {
	phase, _, _ := unstructured.NestedString(backup.Object, "status", "phase")
	reason, _, _ := unstructured.NestedString(backup.Object, "status", "error")
	return phase == "failed" && strings.Contains(reason, HibernatedBackupFailure)
}
