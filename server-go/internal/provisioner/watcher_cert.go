package provisioner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

// cdc_watcher logs in with a client certificate only (EXC-410). The chart
// mounts it from a Secret in the tenant namespace; the stream reads the files
// on every reconnect, and a renewal restarts the pod through the annotation.
const (
	watcherTLSMountPath   = "/etc/watcher-tls"
	watcherTLSVolume      = "client-tls"
	WatcherCertAnnotation = "excalibase.io/client-cert-sha256"
	// 0440 with the chart's fsGroup: readable by the watcher, not by others.
	watcherTLSFileMode = 0o440
)

var ErrWatcherCertificateMissing = errors.New("the watcher has no client certificate to log in with")

// WatcherTLSSecretName is the Secret a project's watcher certificate is stored in.
func WatcherTLSSecretName(projectID string) string {
	return projectID + "-cdc-watcher-tls"
}

// watcherReplicationURL points the stream at the primary with verify-full and
// the mounted certificate files.
func watcherReplicationURL(projectID, namespace, dbName string) string {
	params := url.Values{}
	params.Set("sslmode", "verify-full")
	params.Set("sslcert", watcherTLSMountPath+"/tls.crt")
	params.Set("sslkey", watcherTLSMountPath+"/tls.key")
	params.Set("sslrootcert", watcherTLSMountPath+"/ca.crt")
	params.Set("replication", "database")
	target := url.URL{
		Scheme:   "postgres",
		Host:     fmt.Sprintf("%s-postgres-rw.%s.svc.cluster.local:5432", projectID, namespace),
		Path:     "/" + dbName,
		RawQuery: params.Encode(),
	}
	return target.String()
}

// storeWatcherCertificate writes the certificate Secret, replacing a previous one.
func (p *PostgreSQLProvisioner) storeWatcherCertificate(ctx context.Context, spec WatcherSpec) error {
	if _, err := tenantcert.FromRecord(spec.ClientCert.AddTo(nil)); err != nil {
		return ErrWatcherCertificateMissing
	}
	name := WatcherTLSSecretName(spec.ProjectID)
	data := map[string][]byte{
		"tls.crt": []byte(spec.ClientCert.Cert),
		"tls.key": []byte(spec.ClientCert.Key),
		"ca.crt":  []byte(spec.ClientCert.RootCert),
	}
	err := p.client.CreateSecret(ctx, spec.Namespace, name, data)
	if apierrors.IsAlreadyExists(err) {
		err = p.client.UpdateSecret(ctx, spec.Namespace, name, data)
	}
	if err != nil {
		return fmt.Errorf("store watcher certificate: %w", err)
	}
	return nil
}

// WatcherCertFingerprint is what the pod template carries for a certificate:
// the same value on a running pod means it was deployed with that certificate.
func WatcherCertFingerprint(material tenantcert.Material) string {
	fingerprint := sha256.Sum256([]byte(material.Cert + material.RootCert))
	return hex.EncodeToString(fingerprint[:])
}

// watcherTLSValues mounts the certificate Secret and stamps its fingerprint on
// the pod template.
func watcherTLSValues(spec WatcherSpec) map[string]interface{} {
	return map[string]interface{}{
		"extraVolumes": []interface{}{map[string]interface{}{
			"name": watcherTLSVolume,
			"secret": map[string]interface{}{
				"secretName":  WatcherTLSSecretName(spec.ProjectID),
				"defaultMode": watcherTLSFileMode,
			},
		}},
		"extraVolumeMounts": []interface{}{map[string]interface{}{
			"name":      watcherTLSVolume,
			"mountPath": watcherTLSMountPath,
			"readOnly":  true,
		}},
		"podAnnotations": map[string]interface{}{
			WatcherCertAnnotation: WatcherCertFingerprint(spec.ClientCert),
		},
	}
}
