package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	platformKeyID     = "PLATFORM-MASTER-KEY-ID"
	platformKeySecret = "PLATFORM-MASTER-SECRET-never-in-a-tenant"
)

func withPlatformBackupKey(t *testing.T, svc *ProvisioningService) {
	t.Helper()
	if err := svc.SetBackupDefaults(&BackupDefaults{AccessKeyID: platformKeyID, SecretAccessKey: platformKeySecret, Endpoint: testR2Endpoint, Bucket: "backups"}); err != nil {
		t.Fatalf("SetBackupDefaults: %v", err)
	}
}

// secretsHolding lists every Secret in the fake cluster that carries value.
func secretsHolding(mock *k8s.MockClient, value string) []string {
	var found []string
	for name, data := range mock.Secrets {
		for _, v := range data {
			if bytes.Contains(v, []byte(value)) {
				found = append(found, name)
			}
		}
	}
	return found
}

func projectSecret(t *testing.T, mock *k8s.MockClient, namespace, name string) map[string][]byte {
	t.Helper()
	data, ok := mock.Secrets[namespace+"/"+name]
	if !ok {
		t.Fatalf("secret %s/%s was not written", namespace, name)
	}
	return data
}

func TestAProjectArchivesWithItsOwnTemporaryCredentialsNeverThePlatformKey(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	withPlatformBackupKey(t, svc)
	minter := &recordingMinter{}
	svc.SetBackupCredentials(newTestIssuer(t, minter, newFakeObjectDeleter()))

	setOrgTier(svc, "org1", domain.Standard)
	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{PostgresVersion: "17", ProjectName: "db", OrgID: "org1", DBType: domain.PostgreSQL})
	if err != nil || resp.Status == "FAILED" {
		t.Fatalf("Provision: %v %+v", err, resp)
	}
	if leaked := secretsHolding(mock, platformKeySecret); len(leaked) != 0 {
		t.Fatalf("the platform key reached %v", leaked)
	}
	data := projectSecret(t, mock, resp.Namespace, k8s.BackupCredentialsSecretName)
	prefix := resp.ProjectID + "/cloud/"
	if string(data["ACCESS_KEY_ID"]) != "tmp-"+prefix || string(data[k8s.BackupCredentialsSessionTokenKey]) != "token-"+prefix {
		t.Errorf("the project secret must hold credentials minted for %s, got %v", prefix, data)
	}
	if expiry, err := k8s.BackupCredentialsExpiry(data); err != nil || time.Until(expiry) < 11*time.Hour {
		t.Errorf("expiry %v (%v), want ~12h out", expiry, err)
	}
	store := clusterBackup(t, mock)
	creds, _ := store["s3Credentials"].(map[string]interface{})
	if creds["sessionToken"] == nil {
		t.Errorf("the object store must present the session token, got %v", creds)
	}
}

func TestAProjectWithBackupsIsRefusedWhenNoCredentialProviderIsConfigured(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	withPlatformBackupKey(t, svc)
	svc.SetBackupCredentials(nil)

	err := provisionOnTier(t, svc, domain.Standard, nil)
	if !errors.Is(err, ErrBackupCredentialsNotConfigured) {
		t.Fatalf("err = %v, want ErrBackupCredentialsNotConfigured", err)
	}
	if rows := projectRows(t, svc); rows != 0 || len(mock.CRDs) != 0 {
		t.Errorf("a refused provision left %d rows and %d CRDs", rows, len(mock.CRDs))
	}
	if leaked := secretsHolding(mock, platformKeySecret); len(leaked) != 0 {
		t.Fatalf("the platform key reached %v", leaked)
	}
}

func TestAProjectWhosePrefixHoldsBackupsIsRefused(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	withPlatformBackupKey(t, svc)
	// Whatever id the allocator picks, its prefix already holds objects.
	svc.SetBackupCredentials(newTestIssuer(t, &recordingMinter{}, &prefixAlwaysInUse{}))

	if err := provisionOnTier(t, svc, domain.Standard, nil); !errors.Is(err, ErrBackupPrefixInUse) {
		t.Fatalf("err = %v, want ErrBackupPrefixInUse", err)
	}
	if rows := projectRows(t, svc); rows != 0 || len(mock.CRDs) != 0 {
		t.Errorf("a refused provision left %d rows and %d CRDs", rows, len(mock.CRDs))
	}
}

type prefixAlwaysInUse struct{ fakeObjectDeleter }

func (p *prefixAlwaysInUse) ListKeys(_ context.Context, _, prefix, _ string, _ int32) ([]string, string, error) {
	return []string{prefix + "base/old/data.tar"}, "", nil
}

func TestARequestCannotNameItsOwnBackupStore(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	err := provisionOnTier(t, svc, domain.Standard, &domain.BackupSettings{
		Enabled: true, Schedule: "0 0 2 * * *", Retention: 7,
		S3: &domain.S3Credentials{AccessKeyID: "mine", SecretAccessKey: "mine", Bucket: "backups", Endpoint: testR2Endpoint},
	})
	if !errors.Is(err, ErrBackupStoreChosenByPlatform) {
		t.Fatalf("err = %v, want ErrBackupStoreChosenByPlatform", err)
	}
	if len(mock.CRDs) != 0 {
		t.Error("nothing may be created for a refused request")
	}
}

func TestADockerProjectNeedsNoTemporaryCredentials(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	withPlatformBackupKey(t, svc)
	svc.SetBackupCredentials(nil)
	svc.SetDefaultDeploymentMode(domain.ModeDocker)

	req := &domain.ProvisioningRequest{PostgresVersion: "17", ProjectName: "db", OrgID: "org1", DBType: domain.PostgreSQL}
	setOrgTier(svc, "org1", domain.Standard)
	if _, _, _, err := svc.prepareProvisioning(context.Background(), req); err != nil {
		t.Fatalf("docker mode keeps its store in the platform process and mints nothing: %v", err)
	}
	if req.Backup == nil || req.Backup.S3 == nil || req.Backup.S3.SessionToken != "" {
		t.Errorf("docker backups keep the platform store: %+v", req.Backup)
	}
	if strings.Contains(req.Backup.S3.AccessKeyID, "tmp-") {
		t.Error("docker mode must not mint")
	}
}

func TestARestorePlanCarriesNoStoreKey(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	withPlatformBackupKey(t, svc)
	setOrgTier(svc, "org1", domain.Standard)
	plan, err := svc.RestorePlan(context.Background(), &domain.DatabaseInstance{ProjectID: "src", OrgID: "org1"})
	if err != nil {
		t.Fatalf("RestorePlan: %v", err)
	}
	if plan.Backup == nil || !plan.Backup.Enabled || plan.Backup.S3 != nil {
		t.Fatalf("plan backup %+v: backups on, and no key carried (the adapter mints its own)", plan.Backup)
	}
}
