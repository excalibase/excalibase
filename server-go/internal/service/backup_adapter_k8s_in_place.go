package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// inPlaceTemplateKey is the field of the template Secret holding the
// cluster's definition as JSON.
const inPlaceTemplateKey = "cluster.json"

// ErrInPlaceNothingToRebuild is a replace that finds neither the project's
// cluster nor the definition an interrupted replace saved.
var ErrInPlaceNothingToRebuild = errors.New("the project's database cluster and its saved definition are both missing")

// inPlaceTemplateSecret holds the cluster's definition while it is replaced,
// so a replace interrupted after the delete can rebuild it.
func inPlaceTemplateSecret(projectID string) string { return projectID + "-restore-template" }

// keptClusterSecrets are what the replacement adopts from the deleted
// cluster: the CA every platform client certificate is signed by, and the
// owner's credential.
func keptClusterSecrets(cluster string) []string {
	return []string{cluster + "-ca", cluster + "-app"}
}

var _ InPlaceCluster = (*K8sBackupAdapter)(nil)

// InPlaceRecoveryTarget resolves req against the project's own backups and
// proves a point-in-time target archived. See InPlaceCluster.
func (a *K8sBackupAdapter) InPlaceRecoveryTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (map[string]interface{}, error) {
	target, err := a.recoveryTarget(ctx, inst, req)
	if err != nil {
		return nil, err
	}
	if err := a.ensureTargetRecoverable(ctx, inst, req); err != nil {
		return nil, err
	}
	return target, nil
}

// TakeSafetyBackup takes a backup of the running database and waits for it
// to complete. See InPlaceCluster.
func (a *K8sBackupAdapter) TakeSafetyBackup(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	ref, err := a.TriggerManual(ctx, inst)
	if err != nil {
		return "", err
	}
	err = a.poller.WaitUntilReady(ctx, "safety backup "+ref.ID, func(ctx context.Context) (bool, error) {
		refs, err := a.List(ctx, inst)
		if err != nil {
			log.Printf("in-place restore %s: list backups: %v", inst.ProjectID, err)
			return false, nil
		}
		for _, r := range refs {
			if r.ID != ref.ID {
				continue
			}
			if r.Status == backupStatusFailed {
				return false, fmt.Errorf("backup %s failed: %s", ref.ID, r.Error)
			}
			return r.Status == backupStatusCompleted, nil
		}
		return false, nil
	})
	if err != nil {
		return "", err
	}
	return ref.ID, nil
}

// ReplaceDatabase recreates the project's cluster under its own name,
// recovered from its own archive to target. See InPlaceCluster.
func (a *K8sBackupAdapter) ReplaceDatabase(ctx context.Context, inst *domain.DatabaseInstance, target map[string]interface{}) error {
	name := inst.ProjectID + postgresClusterSuffix
	template, err := a.clusterTemplate(ctx, inst, name)
	if err != nil {
		return err
	}
	replacement, err := k8s.BuildInPlaceRecovery(template, target)
	if err != nil {
		return err
	}
	if err := a.k8sClient.KeepSecretsPastOwner(ctx, inst.Namespace, keptClusterSecrets(name)); err != nil {
		return fmt.Errorf("keep the cluster's certificates: %w", err)
	}
	if err := a.removeCluster(ctx, inst.Namespace, name); err != nil {
		return err
	}
	if err := a.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, replacement); err != nil {
		return fmt.Errorf("create the recovered cluster: %w", err)
	}
	if err := a.waitForRecoveredCluster(ctx, inst.Namespace, inst.ProjectID); err != nil {
		return err
	}
	if err := provisioner.EnsureDocumentDBService(ctx, a.k8sClient, inst.Namespace, inst.ProjectID, inst.DocumentDB); err != nil {
		return err
	}
	if err := a.k8sClient.DeleteSecret(ctx, inst.Namespace, inPlaceTemplateSecret(inst.ProjectID)); err != nil {
		log.Printf("in-place restore %s: remove the saved cluster definition: %v", inst.ProjectID, err)
	}
	return nil
}

// clusterTemplate is the definition the replacement is built from: the one an
// earlier, interrupted replace saved, or else the live cluster, saved now.
func (a *K8sBackupAdapter) clusterTemplate(ctx context.Context, inst *domain.DatabaseInstance, name string) (*unstructured.Unstructured, error) {
	secretName := inPlaceTemplateSecret(inst.ProjectID)
	if saved, err := a.k8sClient.GetSecret(ctx, inst.Namespace, secretName); err == nil && len(saved[inPlaceTemplateKey]) > 0 {
		template := &unstructured.Unstructured{}
		if err := template.UnmarshalJSON(saved[inPlaceTemplateKey]); err != nil {
			return nil, fmt.Errorf("read the saved cluster definition: %w", err)
		}
		return template, nil
	}
	exists, err := a.k8sClient.CRDExists(ctx, k8s.CNPGClusterGVR, inst.Namespace, name)
	if err != nil {
		return nil, fmt.Errorf("read the project's cluster: %w", err)
	}
	if !exists {
		return nil, ErrInPlaceNothingToRebuild
	}
	live, err := a.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, name)
	if err != nil {
		return nil, fmt.Errorf("read the project's cluster: %w", err)
	}
	raw, err := json.Marshal(live.Object)
	if err != nil {
		return nil, fmt.Errorf("save the cluster definition: %w", err)
	}
	if err := a.k8sClient.CreateSecret(ctx, inst.Namespace, secretName, map[string][]byte{inPlaceTemplateKey: raw}); err != nil {
		return nil, fmt.Errorf("save the cluster definition: %w", err)
	}
	return live, nil
}

// removeCluster deletes the cluster and waits until it, its pods and its
// volumes are gone: the replacement takes the same names.
func (a *K8sBackupAdapter) removeCluster(ctx context.Context, namespace, name string) error {
	exists, err := a.k8sClient.CRDExists(ctx, k8s.CNPGClusterGVR, namespace, name)
	if err != nil {
		return fmt.Errorf("read the project's cluster: %w", err)
	}
	if exists {
		if err := a.k8sClient.DeleteCRD(ctx, k8s.CNPGClusterGVR, namespace, name); err != nil {
			return fmt.Errorf("delete the project's cluster: %w", err)
		}
	}
	return a.poller.WaitUntilClear(ctx, "cluster "+name+" removed", func(ctx context.Context) ([]string, error) {
		return a.clusterLeftovers(ctx, namespace, name)
	})
}

// clusterLeftovers names what is left of a deleted cluster.
func (a *K8sBackupAdapter) clusterLeftovers(ctx context.Context, namespace, name string) ([]string, error) {
	left := []string{}
	exists, err := a.k8sClient.CRDExists(ctx, k8s.CNPGClusterGVR, namespace, name)
	if err != nil {
		return nil, err
	}
	if exists {
		left = append(left, "cluster "+name)
	}
	pods, err := a.k8sClient.GetPods(ctx, namespace, "cnpg.io/cluster="+name)
	if err != nil {
		return nil, err
	}
	for _, pod := range pods {
		left = append(left, "pod "+pod.Name)
	}
	claims, err := a.k8sClient.ListPVCs(ctx, namespace)
	if err != nil {
		return nil, err
	}
	for _, claim := range claims {
		if isClusterVolume(claim, name) {
			left = append(left, "volume "+claim)
		}
	}
	return left, nil
}

// isClusterVolume reports whether claim is one of cluster's instance volumes:
// <cluster>-<serial>, or <cluster>-<serial>-wal.
func isClusterVolume(claim, cluster string) bool {
	rest, ok := strings.CutPrefix(claim, cluster+"-")
	if !ok || rest == "" {
		return false
	}
	rest = strings.TrimSuffix(rest, "-wal")
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return rest != ""
}
