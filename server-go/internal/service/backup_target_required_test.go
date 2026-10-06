package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func withBackupTarget(t *testing.T, svc *ProvisioningService) {
	t.Helper()
	if err := svc.SetBackupDefaults(&BackupDefaults{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: testR2Endpoint, Bucket: "backups"}); err != nil {
		t.Fatalf("SetBackupDefaults: %v", err)
	}
	// A Kubernetes platform with a backup target also mints each project's
	// credentials; the target's own key never reaches a namespace.
	svc.SetBackupCredentials(newTestIssuer(t, &recordingMinter{}, newFakeObjectDeleter()))
}

func withoutBackupTarget(t *testing.T) (*ProvisioningService, *k8s.MockClient) {
	t.Helper()
	svc, _, mock := setupProvisioningTest(t)
	if err := svc.SetBackupDefaults(nil); err != nil {
		t.Fatalf("SetBackupDefaults: %v", err)
	}
	return svc, mock
}

func provisionOnTier(t *testing.T, svc *ProvisioningService, tier domain.TierType, backup *domain.BackupSettings) error {
	t.Helper()
	setOrgTier(svc, "org1", tier)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17",
		ProjectName:     "backed-up-db",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Backup:          backup,
	})
	return err
}

func projectRows(t *testing.T, svc *ProvisioningService) int {
	t.Helper()
	all, err := svc.GetAllInstances()
	if err != nil {
		t.Fatalf("GetAllInstances: %v", err)
	}
	return len(all)
}

// clusterBackup is the object store the project's backups go to, or nil when
// the project has none.
func clusterBackup(t *testing.T, mock *k8s.MockClient) map[string]interface{} {
	t.Helper()
	for _, crd := range mock.CRDs {
		if crd.GetKind() == "ObjectStore" {
			configuration, _ := crd.Object["spec"].(map[string]interface{})["configuration"].(map[string]interface{})
			return configuration
		}
	}
	return nil
}

func TestAPaidTierProvisionIsRefusedWithoutABackupTarget(t *testing.T) {
	for name, configure := range map[string]func(*ProvisioningService){
		"nothing configured":  func(*ProvisioningService) {},
		"sealed vault":        func(s *ProvisioningService) { s.SetVault(&sealedVault{}) },
		"vault without entry": func(s *ProvisioningService) { s.SetVault(newFakeVault()) },
		"incomplete vault entry": func(s *ProvisioningService) {
			vault := newFakeVault()
			vault.Put("backup/s3", map[string]string{"accessKeyId": "vk", "bucket": "vb", "endpoint": testR2Endpoint})
			s.SetVault(vault)
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, mock := withoutBackupTarget(t)
			configure(svc)
			err := provisionOnTier(t, svc, domain.Standard, nil)
			if !errors.Is(err, ErrBackupTargetNotConfigured) {
				t.Fatalf("got %v, want ErrBackupTargetNotConfigured", err)
			}
			if rows := projectRows(t, svc); rows != 0 {
				t.Errorf("a refused provision left %d project rows", rows)
			}
			if len(mock.CRDs) != 0 {
				t.Errorf("a refused provision applied %d CRDs", len(mock.CRDs))
			}
		})
	}
}

func TestAnEnabledBackupWithoutATargetIsRefused(t *testing.T) {
	svc, _ := withoutBackupTarget(t)
	err := provisionOnTier(t, svc, domain.Standard, &domain.BackupSettings{Enabled: true, Schedule: "0 2 * * *", Retention: 7})
	if !errors.Is(err, ErrBackupTargetNotConfigured) {
		t.Fatalf("got %v, want ErrBackupTargetNotConfigured", err)
	}
}

func TestAPaidTierProvisionBacksUpToTheVaultTarget(t *testing.T) {
	svc, mock := withoutBackupTarget(t)
	vault := newFakeVault()
	vault.Put("backup/s3", map[string]string{"accessKeyId": "vk", "secretAccessKey": "vs", "bucket": "vb", "endpoint": testR2Endpoint})
	svc.SetVault(vault)

	if err := provisionOnTier(t, svc, domain.Standard, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	backup := clusterBackup(t, mock)
	if backup == nil {
		t.Fatal("a paid-tier project was provisioned without backups")
	}
	if backup["endpointURL"] != testR2Endpoint {
		t.Errorf("endpointURL = %v, want %s", backup["endpointURL"], testR2Endpoint)
	}
}

func tierWithoutBackups() config.TierConfig {
	return config.TierConfig{MaxProjects: 1, Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5", StatementTimeout: "15s"}
}

func TestATierConfiguredWithoutBackupsNeedsNoBackupTarget(t *testing.T) {
	svc, _ := withoutBackupTarget(t)
	svc.SetTierStore(fakeTierStore{m: map[domain.TierType]config.TierConfig{domain.Free: tierWithoutBackups()}})
	if err := provisionOnTier(t, svc, domain.Free, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}
}

func TestAFreeProvisionIsBackedUpLikeEveryOtherTier(t *testing.T) {
	svc, mock := withoutBackupTarget(t)
	if err := provisionOnTier(t, svc, domain.Free, nil); !errors.Is(err, ErrBackupTargetNotConfigured) {
		t.Fatalf("a FREE provision without a backup target: got %v, want ErrBackupTargetNotConfigured", err)
	}
	withBackupTarget(t, svc)
	if err := provisionOnTier(t, svc, domain.Free, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if clusterBackup(t, mock) == nil {
		t.Error("a FREE project was provisioned without backups")
	}
}

func TestPartialBackupDefaultsAreRefused(t *testing.T) {
	for name, defaults := range map[string]BackupDefaults{
		"no secret":   {AccessKeyID: "k", Endpoint: testR2Endpoint, Bucket: "b"},
		"no key":      {SecretAccessKey: "s", Endpoint: testR2Endpoint, Bucket: "b"},
		"no endpoint": {AccessKeyID: "k", SecretAccessKey: "s", Bucket: "b"},
		"no bucket":   {AccessKeyID: "k", SecretAccessKey: "s", Endpoint: testR2Endpoint},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newStorageTestService(t)
			if err := svc.SetBackupDefaults(&defaults); !errors.Is(err, ErrBackupDefaultsIncomplete) {
				t.Fatalf("got %v, want ErrBackupDefaultsIncomplete", err)
			}
		})
	}
}

func TestAbsentBackupDefaultsAreNotAnError(t *testing.T) {
	svc := newStorageTestService(t)
	if err := svc.SetBackupDefaults(&BackupDefaults{Bucket: "excalibase-backups", Region: "auto"}); err != nil {
		t.Fatalf("got %v", err)
	}
	if _, ok := svc.BackupStorage(); ok {
		t.Error("no credentials must mean no backup target")
	}
}
