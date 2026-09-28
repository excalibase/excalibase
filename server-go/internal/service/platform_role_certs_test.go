package service

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

func verifyRoleCertificate(t *testing.T, record map[string]string, role string) {
	t.Helper()
	material, err := tenantcert.FromRecord(record)
	if err != nil {
		t.Fatalf("%s record: %v", role, err)
	}
	clusterCA := string(k8s.MockClusterCA()["ca.crt"])
	if material.RootCert != clusterCA {
		t.Errorf("%s root is not the cluster CA", role)
	}
	block, _ := pem.Decode([]byte(material.Cert))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != role {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, role)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(clusterCA))
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("%s certificate does not chain to the cluster CA: %v", role, err)
	}
}

// pg_hba admits the platform roles by certificate only (EXC-410), so each
// one's vault record carries the certificate its clients log in with.
func TestRegistrationFilesAClientCertificateForEveryPlatformRole(t *testing.T) {
	h := newRegistrationHarness(t)
	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	for _, role := range []string{roleAuthAdmin, roleApp, roleWatcher} {
		record, err := h.vault.Get(vaultCredentialPath(testRegProject, role))
		if err != nil {
			t.Fatal(err)
		}
		verifyRoleCertificate(t, record, role)
	}
	owner, _ := h.vault.Get(vaultCredentialPath(testRegProject, roleAdmin))
	if _, err := tenantcert.FromRecord(owner); err == nil {
		t.Error("the customer's own role logs in with its password; it gets no platform certificate")
	}
}

func TestRegistrationFailsWithoutTheClusterCA(t *testing.T) {
	h := newRegistrationHarness(t)
	h.kube.NoClusterCA = true
	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{}); err == nil {
		t.Fatal("a project whose platform roles cannot log in was registered")
	}
}

func withWatcherProvisioner(h *registrationHarness) {
	pg := provisioner.NewPostgreSQLProvisioner(h.kube, "/charts/excalibase-watcher")
	pg.SetWatcherImage("excalibase/excalibase-watcher-go:1.0.0")
	h.svc.factory = provisioner.NewFactory(pg)
	h.svc.SetNatsCredentialMinter(NewNatsCredentialMinter(newFakeNatsCredStore()))
}

func TestTheWatcherIsDeployedWithTheCertificateFiledForIt(t *testing.T) {
	h := newRegistrationHarness(t)
	withWatcherProvisioner(h)
	inst := restoredInstance()
	if err := h.svc.RegisterProject(context.Background(), inst, RegistrationOptions{}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	record, _ := h.vault.Get(vaultCredentialPath(testRegProject, roleWatcher))
	secret := h.kube.Secrets[inst.Namespace+"/"+testRegProject+"-cdc-watcher-tls"]
	if string(secret["tls.crt"]) != record[tenantcert.FieldCert] || string(secret["tls.key"]) != record[tenantcert.FieldKey] {
		t.Error("the watcher does not present the certificate filed for cdc_watcher")
	}
}

func TestRestartReplicationRefusesAWatcherRecordWithoutACertificate(t *testing.T) {
	h := newRegistrationHarness(t)
	withWatcherProvisioner(h)
	inst := restoredInstance()
	if err := h.vault.Put(vaultCredentialPath(inst.ProjectID, roleWatcher), map[string]string{"password": "watcher-pw"}); err != nil {
		t.Fatal(err)
	}
	err := h.svc.RestartReplication(context.Background(), inst)
	if !errors.Is(err, tenantcert.ErrNoClientCertificate) {
		t.Fatalf("err = %v, want ErrNoClientCertificate", err)
	}
}

func seedWatcherRecord(t *testing.T, h *registrationHarness, projectID string) {
	t.Helper()
	ca := k8s.MockClusterCA()
	material, err := tenantcert.Issue(tenantcert.CA{CertPEM: ca["ca.crt"], KeyPEM: ca["ca.key"]}, roleWatcher, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.vault.Put(vaultCredentialPath(projectID, roleWatcher), material.AddTo(map[string]string{"password": "watcher-pw"})); err != nil {
		t.Fatal(err)
	}
}
