package service

import (
	"bytes"
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// clusterOwnerSecretName is the Secret CloudNativePG keeps the owner login in.
func clusterOwnerSecretName(projectID string) string { return projectID + "-postgres-app" }

// syncClusterOwnerSecret files the owner's password in the cluster's own owner
// Secret. CloudNativePG applies that Secret's password to the owner role each
// time an instance manager starts as primary, so after a failover the role
// holds whatever the Secret holds. A password the customer was given that is
// not in the Secret is replaced by the one from creation at the first failover.
// Only Postgres on Kubernetes has such a Secret; a Secret filed under another
// login is left alone.
func (s *ProvisioningService) syncClusterOwnerSecret(ctx context.Context, inst *domain.DatabaseInstance, username, password string) error {
	if inst.DBType == domain.MySQL || inst.DeploymentMode == domain.ModeDocker || s.k8sClient == nil {
		return nil
	}
	name := clusterOwnerSecretName(inst.ProjectID)
	secret, err := s.k8sClient.GetSecret(ctx, inst.Namespace, name)
	if err != nil {
		return fmt.Errorf("read the owner secret of %s: %w", inst.ProjectID, err)
	}
	if string(secret["username"]) != username {
		return nil
	}
	old := secret["password"]
	updated := make(map[string][]byte, len(secret))
	for key, value := range secret {
		// uri, jdbc-uri and pgpass embed the password.
		if len(old) > 0 && key != "password" {
			value = bytes.ReplaceAll(value, old, []byte(password))
		}
		updated[key] = value
	}
	updated["password"] = []byte(password)
	if err := s.k8sClient.UpdateSecret(ctx, inst.Namespace, name, updated); err != nil {
		return fmt.Errorf("file the owner password of %s in its cluster secret: %w", inst.ProjectID, err)
	}
	return nil
}
