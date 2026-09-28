package service

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

const renewProject = "proj-renew"

type renewalHarness struct {
	renewer   *RoleCertificateRenewer
	store     *storage.FileSystemStore
	vault     *fakeVault
	kube      *k8s.MockClient
	restarted []string
	now       time.Time
}

func newRenewalHarness(t *testing.T, status string) *renewalHarness {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: renewProject, Namespace: "ns-renew", DBType: domain.PostgreSQL,
		DeploymentMode: domain.ModeK8s, Status: status,
	}); err != nil {
		t.Fatal(err)
	}
	h := &renewalHarness{store: store, vault: newFakeVault(), kube: k8s.NewMockClient(), now: time.Now()}
	ca := k8s.MockClusterCA()
	for _, role := range tenantcert.PlatformRoles {
		material, err := tenantcert.Issue(tenantcert.CA{CertPEM: ca["ca.crt"], KeyPEM: ca["ca.key"]}, role, h.now)
		if err != nil {
			t.Fatal(err)
		}
		h.vault.data[vaultCredentialPath(renewProject, role)] = material.AddTo(map[string]string{"username": role, "password": "pw"})
	}
	h.renewer = NewRoleCertificateRenewer(RoleCertificateRenewerConfig{
		Instances: store, Kube: h.kube, Vault: h.vault,
		RestartReplication: func(_ context.Context, inst *domain.DatabaseInstance) error {
			h.restarted = append(h.restarted, inst.ProjectID)
			return nil
		},
		Now: func() time.Time { return h.now },
	})
	return h
}

func (h *renewalHarness) notAfter(t *testing.T, role string) time.Time {
	t.Helper()
	block, _ := pem.Decode([]byte(h.vault.data[vaultCredentialPath(renewProject, role)][tenantcert.FieldCert]))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert.NotAfter
}

func TestAFreshCertificateIsLeftAlone(t *testing.T) {
	h := newRenewalHarness(t, "ACTIVE")
	before := h.vault.data[vaultCredentialPath(renewProject, roleApp)][tenantcert.FieldCert]
	report := h.renewer.RenewDue(context.Background())
	if report.Renewed != 0 || len(report.Failed) != 0 || len(h.restarted) != 0 {
		t.Fatalf("report %+v, restarted %v", report, h.restarted)
	}
	if h.vault.data[vaultCredentialPath(renewProject, roleApp)][tenantcert.FieldCert] != before {
		t.Error("a fresh certificate was replaced")
	}
}

// Inside the window every role is renewed, keeping the rest of its record,
// and the watcher is redeployed onto its new certificate.
func TestACertificateInsideTheWindowIsRenewed(t *testing.T) {
	h := newRenewalHarness(t, "ACTIVE")
	old := h.notAfter(t, roleApp)
	h.now = h.now.Add(tenantcert.Validity - tenantcert.RenewBefore + time.Hour)

	report := h.renewer.RenewDue(context.Background())
	if report.Renewed != 1 || len(report.Failed) != 0 {
		t.Fatalf("report %+v", report)
	}
	for _, role := range tenantcert.PlatformRoles {
		if !h.notAfter(t, role).After(old) {
			t.Errorf("%s not renewed", role)
		}
		if h.vault.data[vaultCredentialPath(renewProject, role)]["username"] != role {
			t.Errorf("%s record lost its other fields", role)
		}
	}
	if len(h.restarted) != 1 {
		t.Errorf("watcher restarts = %v", h.restarted)
	}
}

func TestAChangedClusterCAIsRepublished(t *testing.T) {
	h := newRenewalHarness(t, "ACTIVE")
	path := vaultCredentialPath(renewProject, roleAuthAdmin)
	h.vault.data[path][tenantcert.FieldRootCert] = "an older CA certificate"
	if report := h.renewer.RenewDue(context.Background()); report.Renewed != 1 {
		t.Fatalf("report %+v", report)
	}
	if h.vault.data[path][tenantcert.FieldRootCert] != string(k8s.MockClusterCA()["ca.crt"]) {
		t.Error("the current cluster CA was not republished")
	}
}

// A paused project has no watcher running; resume deploys it from vault.
func TestAPausedProjectIsRenewedWithoutStartingItsWatcher(t *testing.T) {
	h := newRenewalHarness(t, string(domain.StatusPaused))
	h.now = h.now.Add(tenantcert.Validity)
	if report := h.renewer.RenewDue(context.Background()); report.Renewed != 1 {
		t.Fatalf("report %+v", report)
	}
	if len(h.restarted) != 0 {
		t.Errorf("a paused project's watcher was started: %v", h.restarted)
	}
}

func TestAProjectWithoutItsClusterCAIsReportedFailed(t *testing.T) {
	h := newRenewalHarness(t, "ACTIVE")
	h.kube.NoClusterCA = true
	if report := h.renewer.RenewDue(context.Background()); len(report.Failed) != 1 {
		t.Fatalf("report %+v", report)
	}
}

func TestADockerProjectHasNoCertificatesToRenew(t *testing.T) {
	h := newRenewalHarness(t, "ACTIVE")
	inst, _ := h.store.FindByProjectID(renewProject)
	inst.DeploymentMode = domain.ModeDocker
	if err := h.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(tenantcert.Validity)
	if report := h.renewer.RenewDue(context.Background()); report.Renewed != 0 || len(report.Failed) != 0 {
		t.Fatalf("report %+v", report)
	}
}
