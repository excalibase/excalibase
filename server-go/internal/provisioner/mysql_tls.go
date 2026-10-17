package provisioner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/mysqltls"
)

// A MySQL project's server certificate comes from its own cert-manager CA
// (EXC-589); see k8s.BuildMySQLTLS.

// mysqlReloadTLS makes mysqld read its renewed certificate. Sessions already
// open keep theirs; only new connections see the new one.
const mysqlReloadTLS = "ALTER INSTANCE RELOAD TLS"

// SetPublicDomainSuffix names the suffix under which a project's public
// endpoint is dialled, so its server certificate can carry that name.
func (p *MySQLProvisioner) SetPublicDomainSuffix(suffix string) { p.publicDomainSuffix = suffix }

// applyTLS creates or updates the project's CA and server certificate.
func (p *MySQLProvisioner) applyTLS(ctx context.Context, opts k8s.MySQLClusterOpts) error {
	public, err := PublicServerNames(opts.ProjectID, p.publicDomainSuffix)
	if err != nil {
		return err
	}
	opts.PublicNames = public
	for _, object := range k8s.BuildMySQLTLS(opts) {
		if err := p.client.ApplyCRD(ctx, object.GVR, opts.Namespace, object.Object); err != nil {
			return fmt.Errorf("apply %s %s (is cert-manager installed?): %w", object.Object.GetKind(), object.Object.GetName(), err)
		}
	}
	return nil
}

// ServerCA is the PEM of the CA the project's server certificate is signed
// by, what every client verifies the server with.
func (p *MySQLProvisioner) ServerCA(ctx context.Context, namespace, projectID string) (string, error) {
	secret, err := p.client.GetSecret(ctx, namespace, k8s.MySQLCASecretName(projectID))
	if err != nil {
		return "", fmt.Errorf("read the MySQL CA of %s: %w", projectID, err)
	}
	return k8s.MySQLServerCA(secret)
}

// ServerCertificateExpiry is when the certificate cert-manager last issued
// for the project runs out.
func (p *MySQLProvisioner) ServerCertificateExpiry(ctx context.Context, namespace, projectID string) (time.Time, error) {
	secret, err := p.client.GetSecret(ctx, namespace, k8s.MySQLServerTLSSecretName(projectID))
	if err != nil {
		return time.Time{}, fmt.Errorf("read the MySQL server certificate of %s: %w", projectID, err)
	}
	return mysqltls.CertificateNotAfter(string(secret["tls.crt"]))
}

// ReloadTLS has every running instance read its certificate again. An
// instance that is not ready reads it when it starts.
func (p *MySQLProvisioner) ReloadTLS(ctx context.Context, namespace, projectID string) error {
	instances, err := p.clusterInstances(ctx, namespace, projectID)
	if err != nil {
		return err
	}
	var failed []error
	for i := 0; i < instances; i++ {
		pod := k8s.MySQLPodName(projectID, int64(i))
		ready, err := p.client.IsPodReady(ctx, namespace, pod)
		if err != nil {
			failed = append(failed, fmt.Errorf("read the readiness of %s: %w", pod, err))
			continue
		}
		if !ready {
			continue
		}
		cmd := []string{"mysql", "--defaults-extra-file=" + mysqlAdminCnf, "--batch", "-e", mysqlReloadTLS}
		if _, err := p.client.ExecInPod(ctx, namespace, pod, mysqlContainer, cmd); err != nil {
			failed = append(failed, fmt.Errorf("reload TLS on %s: %w", pod, err))
		}
	}
	return errors.Join(failed...)
}

// removeTLS deletes the project's certificates, issuers and their secrets.
func (p *MySQLProvisioner) removeTLS(ctx context.Context, namespace, projectID string) error {
	objects := k8s.BuildMySQLTLS(k8s.MySQLClusterOpts{ProjectID: projectID, Namespace: namespace})
	for i := len(objects) - 1; i >= 0; i-- {
		object := objects[i]
		if err := p.client.DeleteCRD(ctx, object.GVR, namespace, object.Object.GetName()); err != nil {
			return fmt.Errorf("delete %s %s: %w", object.Object.GetKind(), object.Object.GetName(), err)
		}
	}
	for _, secret := range []string{k8s.MySQLServerTLSSecretName(projectID), k8s.MySQLCASecretName(projectID)} {
		if err := p.client.DeleteSecret(ctx, namespace, secret); err != nil {
			return err
		}
	}
	return nil
}
