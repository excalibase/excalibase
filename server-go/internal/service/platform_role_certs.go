package service

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

// CNPG keeps each cluster's CA in "<cluster>-ca". By default it is both the
// server CA and the client CA pg_hba's "cert" lines verify against.
const (
	clusterCASecretSuffix = "-postgres-ca"
	clusterCACertKey      = "ca.crt"
	clusterCAKeyKey       = "ca.key"
)

// readClusterCA reads the cluster's CA; a missing or partial one is an error,
// since without it no platform role could log in.
func readClusterCA(ctx context.Context, kube k8s.KubeClient, namespace, projectID string) (tenantcert.CA, error) {
	secret, err := kube.GetSecret(ctx, namespace, projectID+clusterCASecretSuffix)
	if err != nil {
		return tenantcert.CA{}, fmt.Errorf("read cluster CA for %s: %w", projectID, err)
	}
	ca := tenantcert.CA{CertPEM: secret[clusterCACertKey], KeyPEM: secret[clusterCAKeyKey]}
	if len(ca.CertPEM) == 0 || len(ca.KeyPEM) == 0 {
		return tenantcert.CA{}, fmt.Errorf("cluster CA for %s: %w", projectID, tenantcert.ErrInvalidCA)
	}
	return ca, nil
}

// issueRoleCertificates signs a certificate for every platform role of a
// Kubernetes project. A project without Kubernetes has no pg_hba of ours and
// gets none.
func (s *ProvisioningService) issueRoleCertificates(ctx context.Context, namespace, projectID string) (map[string]tenantcert.Material, error) {
	if s.k8sClient == nil {
		return nil, nil
	}
	ca, err := readClusterCA(ctx, s.k8sClient, namespace, projectID)
	if err != nil {
		return nil, err
	}
	certificates := make(map[string]tenantcert.Material, len(k8s.PlatformCertRoles))
	for _, role := range k8s.PlatformCertRoles {
		material, err := tenantcert.Issue(ca, role, time.Now())
		if err != nil {
			return nil, fmt.Errorf("issue %s certificate: %w", role, err)
		}
		certificates[role] = material
	}
	return certificates, nil
}
