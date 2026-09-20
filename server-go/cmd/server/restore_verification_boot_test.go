package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// bootVault is a stand-in VaultClient: boot wiring only needs one to exist,
// and no test here reads a credential through it.
type bootVault struct{}

func (bootVault) Get(string) (map[string]string, error) { return map[string]string{}, nil }
func (bootVault) Put(string, map[string]string) error   { return nil }
func (bootVault) Delete(string) error                   { return nil }
func (bootVault) DeletePrefix(string) (int, error)      { return 0, nil }
func (bootVault) List(string) ([]string, error)         { return nil, nil }
func (bootVault) Sealed() bool                          { return false }
func (bootVault) GetPublicKey() (string, error)         { return "", nil }

// unverifiableAdapter is a BackupAdapter that cannot take a database probe —
// the shape a future or legacy adapter would have if it forgot to.
type unverifiableAdapter struct{}

func (unverifiableAdapter) Configure(context.Context, *domain.DatabaseInstance, string, int) error {
	return nil
}

func (unverifiableAdapter) TriggerManual(context.Context, *domain.DatabaseInstance) (service.BackupRef, error) {
	return service.BackupRef{}, nil
}

func (unverifiableAdapter) List(context.Context, *domain.DatabaseInstance) ([]service.BackupRef, error) {
	return nil, nil
}

func (unverifiableAdapter) BackupsConfigured() bool { return true }

func (unverifiableAdapter) Restore(context.Context, *domain.DatabaseInstance, domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	return nil, nil
}

const bootReadyTimeout = 15 * time.Minute

func bootBackupService(t *testing.T, modes ...domain.DeploymentMode) *service.BackupService {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	storageSource := service.StaticBackupStorage(&domain.S3Credentials{
		AccessKeyID: "k", SecretAccessKey: "s", Endpoint: "https://r2", Bucket: "b",
	})
	adapters := map[domain.DeploymentMode]service.BackupAdapter{}
	for _, mode := range modes {
		switch mode {
		case domain.ModeK8s:
			k8sAdapter := service.NewK8sBackupAdapter(k8s.NewMockClient(), t.TempDir(), storageSource)
			k8sAdapter.SetInstanceStore(store)
			adapters[domain.ModeK8s] = k8sAdapter
		case domain.ModeDocker:
			dockerAdapter := service.NewDockerBackupAdapter(service.DockerBackupAdapterConfig{
				Bucket: "b", Instances: store,
			})
			adapters[domain.ModeDocker] = dockerAdapter
		}
	}
	return service.NewBackupServiceWithAdapters(store, adapters, t.TempDir())
}

// Every adapter the platform can restore through must take the probe at boot.
// Without it the adapter refuses at restore time, which is a customer finding
// out that the platform was misconfigured.
func TestRestoreVerificationWiresBothAdapters(t *testing.T) {
	svc := bootBackupService(t, domain.ModeK8s, domain.ModeDocker)

	if err := wireRestoreVerification(svc, bootVault{}, bootReadyTimeout); err != nil {
		t.Fatalf("wireRestoreVerification: %v", err)
	}
}

func TestRestoreVerificationFailsAtBootWithoutAVault(t *testing.T) {
	svc := bootBackupService(t, domain.ModeK8s, domain.ModeDocker)

	err := wireRestoreVerification(svc, nil, bootReadyTimeout)
	if err == nil {
		t.Fatal("a platform whose probe has no vault to read must not start")
	}
	if !strings.Contains(err.Error(), "vault") {
		t.Errorf("err: got %v, want it to name the missing vault", err)
	}
}

func TestRestoreVerificationFailsAtBootWithNoAdapters(t *testing.T) {
	svc := bootBackupService(t)

	err := wireRestoreVerification(svc, bootVault{}, bootReadyTimeout)
	if !errors.Is(err, service.ErrDatabaseProbeNotConfigured) {
		t.Fatalf("err: got %v, want ErrDatabaseProbeNotConfigured", err)
	}
}

func TestRestoreVerificationFailsAtBootForAnAdapterThatCannotVerify(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	svc := service.NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]service.BackupAdapter{
		domain.DeploymentMode("legacy"): unverifiableAdapter{},
	}, t.TempDir())

	err = wireRestoreVerification(svc, bootVault{}, bootReadyTimeout)
	if !errors.Is(err, service.ErrDatabaseProbeNotConfigured) {
		t.Fatalf("err: got %v, want ErrDatabaseProbeNotConfigured", err)
	}
	if !strings.Contains(err.Error(), "legacy") {
		t.Errorf("err must name the adapter that cannot verify: %v", err)
	}
}
