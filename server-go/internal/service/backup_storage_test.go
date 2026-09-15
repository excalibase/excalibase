package service

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func newStorageTestService(t *testing.T) *ProvisioningService {
	t.Helper()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	return NewProvisioningService(store, provisioner.NewFactory(), k8s.NewMockClient())
}

func TestBackupStorageUsesPlatformDefaults(t *testing.T) {
	svc := newStorageTestService(t)
	svc.SetBackupDefaults(&BackupDefaults{
		AccessKeyID: "k", SecretAccessKey: "s", Endpoint: testR2Endpoint, Bucket: "excalibase-backups",
	})

	got, ok := svc.BackupStorage()
	if !ok {
		t.Fatal("expected storage from platform defaults")
	}
	want := domain.S3Credentials{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: testR2Endpoint, Bucket: "excalibase-backups", Region: "auto"}
	if *got != want {
		t.Errorf("storage: got %+v, want %+v", *got, want)
	}
}

func TestBackupStorageDefaultsWinOverVault(t *testing.T) {
	// Mirrors Provision: applyBackupDefaults fills req.Backup.S3 before the
	// vault lookup runs, so env defaults take precedence when both exist.
	svc := newStorageTestService(t)
	vault := newFakeVault()
	vault.Put("backup/s3", map[string]string{"accessKeyId": "vk", "secretAccessKey": "vs", "bucket": "vb", "endpoint": testLocalstackEndpoint})
	svc.SetVault(vault)
	svc.SetBackupDefaults(&BackupDefaults{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: testR2Endpoint, Bucket: "b"})

	got, _ := svc.BackupStorage()
	if got.Endpoint != testR2Endpoint {
		t.Errorf("endpoint: got %s, want platform default %s", got.Endpoint, testR2Endpoint)
	}
}

func TestBackupStorageFallsBackToVault(t *testing.T) {
	svc := newStorageTestService(t)
	vault := newFakeVault()
	vault.Put("backup/s3", map[string]string{"accessKeyId": "vk", "secretAccessKey": "vs", "bucket": "vb", "region": "auto", "endpoint": testR2Endpoint})
	svc.SetVault(vault)

	got, ok := svc.BackupStorage()
	if !ok {
		t.Fatal("expected storage from vault backup/s3")
	}
	if got.AccessKeyID != "vk" || got.Bucket != "vb" || got.Endpoint != testR2Endpoint {
		t.Errorf("storage: got %+v", *got)
	}
}

func TestBackupStorageNoneConfigured(t *testing.T) {
	for name, vault := range map[string]func() *ProvisioningService{
		"no vault":       func() *ProvisioningService { return newStorageTestService(t) },
		"sealed vault":   func() *ProvisioningService { s := newStorageTestService(t); s.SetVault(&sealedVault{}); return s },
		"vault no entry": func() *ProvisioningService { s := newStorageTestService(t); s.SetVault(newFakeVault()); return s },
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := vault().BackupStorage(); ok {
				t.Errorf("expected no storage, got %+v", *got)
			}
		})
	}
}

func TestStaticBackupStorage(t *testing.T) {
	if _, ok := StaticBackupStorage(nil).BackupStorage(); ok {
		t.Error("nil static storage must report not configured")
	}
	cfg := &domain.S3Credentials{AccessKeyID: "k", SecretAccessKey: "s", Bucket: "b"}
	if got, ok := StaticBackupStorage(cfg).BackupStorage(); !ok || got.Bucket != "b" {
		t.Errorf("static storage: ok=%v got=%+v", ok, got)
	}
}
