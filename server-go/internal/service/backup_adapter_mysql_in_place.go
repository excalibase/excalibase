package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// An in-place MySQL restore (ADR 0041 decision 6) rebuilds the project's
// cluster under the same name from its own prefix: the endpoint, vault
// records and backup policy stay; restore_in_place.go drives the contract.

var _ InPlaceCluster = (*MySQLBackupAdapter)(nil)

const mysqlRestorePointKey = "restorePoint"

// InPlaceRecoveryTarget resolves req against the project's own runs.
func (a *MySQLBackupAdapter) InPlaceRecoveryTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (map[string]interface{}, error) {
	backups, err := a.backups(ctx, inst)
	if err != nil {
		return nil, err
	}
	point, err := resolveMySQLRestorePoint(backups, req)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{mysqlRestorePointKey: point.UTC().Format(time.RFC3339)}, nil
}

// TakeSafetyBackup runs a backup now, waits for it, and names the run it made.
func (a *MySQLBackupAdapter) TakeSafetyBackup(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	ref, err := a.TriggerManual(ctx, inst)
	if err != nil {
		return "", err
	}
	err = a.poller.WaitUntilReady(ctx, "safety backup "+ref.ID, func(ctx context.Context) (bool, error) {
		status, err := a.BackupStatusOf(ctx, inst, ref.ID)
		if err != nil {
			log.Printf("in-place restore %s: read safety backup: %v", inst.ProjectID, err)
			return false, nil
		}
		if status == backupStatusFailed {
			return false, fmt.Errorf("backup %s failed", ref.ID)
		}
		return status == backupStatusCompleted, nil
	})
	if err != nil {
		return "", err
	}
	return a.runOfJob(ctx, inst, ref.ID)
}

// runOfJob is the run a finished backup Job wrote: the one it started and
// finished around.
func (a *MySQLBackupAdapter) runOfJob(ctx context.Context, inst *domain.DatabaseInstance, jobName string) (string, error) {
	job, err := a.kube.GetCRD(ctx, k8s.JobGVR, inst.Namespace, jobName)
	if err != nil {
		return "", fmt.Errorf("read backup job %s: %w", jobName, err)
	}
	state := k8s.MySQLBackupJobState(job)
	backups, err := a.backups(ctx, inst)
	if err != nil {
		return "", err
	}
	for _, backup := range completedMySQLBackups(backups) {
		if !backup.At.Before(state.Started.Truncate(time.Second)) && !backup.At.After(state.Finished) {
			return backup.ID, nil
		}
	}
	return "", fmt.Errorf("backup job %s finished but its run is not in the bucket", jobName)
}

// ReplaceDatabase deletes the project's cluster, waits until its pods and
// volumes are gone, and recreates it under the same name restored to target.
func (a *MySQLBackupAdapter) ReplaceDatabase(ctx context.Context, inst *domain.DatabaseInstance, target map[string]interface{}) error {
	raw, _ := target[mysqlRestorePointKey].(string)
	point, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fmt.Errorf("in-place restore %s: unreadable restore point %q", inst.ProjectID, raw)
	}
	template, err := a.clusterTemplate(ctx, inst)
	if err != nil {
		return err
	}
	job, err := a.inPlaceJobOpts(inst, template)
	if err != nil {
		return err
	}
	if err := a.removeCluster(ctx, inst); err != nil {
		return err
	}
	source := k8s.MySQLRestoreSource{
		Namespace: inst.Namespace, ProjectID: inst.ProjectID, Point: point,
		CredentialsSecret: k8s.MySQLBackupCredentialsSecret,
	}
	if err := a.mysql.ApplyRestore(ctx, freshMySQLCluster(template), source, job); err != nil {
		return err
	}
	if err := a.mysql.AwaitRestored(ctx, a.poller, inst.Namespace, inst.ProjectID, mysqlReplicas(template)); err != nil {
		return err
	}
	if err := a.mysql.LinkBackupPolicy(ctx, inst.Namespace, inst.ProjectID); err != nil {
		return err
	}
	if err := a.kube.DeleteSecret(ctx, inst.Namespace, inPlaceTemplateSecret(inst.ProjectID)); err != nil {
		log.Printf("in-place restore %s: remove the saved cluster definition: %v", inst.ProjectID, err)
	}
	return nil
}

// inPlaceJobOpts sizes the restore job from the cluster it rebuilds.
func (a *MySQLBackupAdapter) inPlaceJobOpts(inst *domain.DatabaseInstance, template *unstructured.Unstructured) (k8s.MySQLBackupOpts, error) {
	store, ok := a.storage.BackupStorage()
	if !ok {
		return k8s.MySQLBackupOpts{}, ErrBackupStorageNotConfigured
	}
	disk, err := k8s.MySQLClusterDisk(template)
	if err != nil {
		return k8s.MySQLBackupOpts{}, err
	}
	cpu, memory := k8s.MySQLClusterResources(template)
	class, _, _ := unstructured.NestedString(template.Object, "spec", "volumeClaimTemplates", "0", "spec", "storageClassName")
	return k8s.MySQLBackupOpts{
		ProjectID: inst.ProjectID, Namespace: inst.Namespace,
		Plan: config.TierConfig{Instances: int(mysqlReplicas(template)), CPU: cpu, Memory: memory},
		Disk: disk.String(), StorageClass: class, Store: a.mysql.BackupStore(store),
	}, nil
}

func mysqlReplicas(cluster *unstructured.Unstructured) int {
	replicas, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas")
	return int(max(replicas, 1))
}

// freshMySQLCluster is the definition to create again: name, labels and spec,
// without the deleted object's identity, status, policy link or old restore.
func freshMySQLCluster(template *unstructured.Unstructured) *unstructured.Unstructured {
	spec, _, _ := unstructured.NestedMap(template.Object, "spec")
	delete(spec, "restore")
	delete(spec, "backupPolicyName")
	fresh := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": template.GetAPIVersion(), "kind": template.GetKind(), "spec": spec,
	}}
	fresh.SetName(template.GetName())
	fresh.SetNamespace(template.GetNamespace())
	fresh.SetLabels(template.GetLabels())
	return fresh
}

// clusterTemplate is the definition an interrupted replace saved, or else the
// live cluster, saved now.
func (a *MySQLBackupAdapter) clusterTemplate(ctx context.Context, inst *domain.DatabaseInstance) (*unstructured.Unstructured, error) {
	secret := inPlaceTemplateSecret(inst.ProjectID)
	if saved, err := a.kube.GetSecret(ctx, inst.Namespace, secret); err == nil && len(saved[inPlaceTemplateKey]) > 0 {
		template := &unstructured.Unstructured{}
		if err := template.UnmarshalJSON(saved[inPlaceTemplateKey]); err != nil {
			return nil, fmt.Errorf("read the saved cluster definition: %w", err)
		}
		return template, nil
	}
	name := k8s.MySQLClusterName(inst.ProjectID)
	exists, err := a.kube.CRDExists(ctx, k8s.MOCOClusterGVR, inst.Namespace, name)
	if err != nil {
		return nil, fmt.Errorf("read the project's cluster: %w", err)
	}
	if !exists {
		return nil, ErrInPlaceNothingToRebuild
	}
	live, err := a.kube.GetCRD(ctx, k8s.MOCOClusterGVR, inst.Namespace, name)
	if err != nil {
		return nil, fmt.Errorf("read the project's cluster: %w", err)
	}
	raw, err := json.Marshal(live.Object)
	if err != nil {
		return nil, fmt.Errorf("save the cluster definition: %w", err)
	}
	if err := a.kube.CreateSecret(ctx, inst.Namespace, secret, map[string][]byte{inPlaceTemplateKey: raw}); err != nil {
		return nil, fmt.Errorf("save the cluster definition: %w", err)
	}
	return live, nil
}

// removeCluster deletes the cluster and waits until it, its pods, volumes and
// restore Job are gone: the replacement takes the same names.
func (a *MySQLBackupAdapter) removeCluster(ctx context.Context, inst *domain.DatabaseInstance) error {
	name := k8s.MySQLClusterName(inst.ProjectID)
	if err := a.kube.DeleteCRD(ctx, k8s.MOCOClusterGVR, inst.Namespace, name); err != nil {
		return fmt.Errorf("delete the project's cluster: %w", err)
	}
	return a.poller.WaitUntilClear(ctx, "cluster "+name+" removed", func(ctx context.Context) ([]string, error) {
		return a.clusterLeftovers(ctx, inst)
	})
}

func (a *MySQLBackupAdapter) clusterLeftovers(ctx context.Context, inst *domain.DatabaseInstance) ([]string, error) {
	left := []string{}
	cluster, restoreJob := k8s.MySQLClusterName(inst.ProjectID), k8s.MySQLRestoreJobName(inst.ProjectID)
	exists, err := a.kube.CRDExists(ctx, k8s.MOCOClusterGVR, inst.Namespace, cluster)
	if err != nil {
		return nil, err
	}
	if exists {
		left = append(left, "cluster "+cluster)
	}
	if exists, err = a.kube.CRDExists(ctx, k8s.JobGVR, inst.Namespace, restoreJob); err != nil {
		return nil, err
	}
	if exists {
		left = append(left, "job "+restoreJob)
	}
	pods, err := a.kube.GetPods(ctx, inst.Namespace, k8s.MySQLPodSelector(inst.ProjectID))
	if err != nil {
		return nil, err
	}
	for _, pod := range pods {
		left = append(left, "pod "+pod.Name)
	}
	claims, err := a.kube.ListPVCs(ctx, inst.Namespace)
	if err != nil {
		return nil, err
	}
	for _, claim := range claims {
		if strings.HasPrefix(claim, k8s.MySQLDataClaimPrefix(inst.ProjectID)) {
			left = append(left, "volume "+claim)
		}
	}
	return left, nil
}
