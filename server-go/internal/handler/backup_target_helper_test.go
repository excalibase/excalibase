package handler

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/objectcreds"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// withBackupTarget gives the service the backup target every tier requires,
// and what mints each project's temporary credentials from it.
func withBackupTarget(t *testing.T, svc *service.ProvisioningService) {
	t.Helper()
	if err := svc.SetBackupDefaults(&service.BackupDefaults{
		AccessKeyID: "k", SecretAccessKey: "s", Endpoint: "https://backups.example.test", Bucket: "backups",
	}); err != nil {
		t.Fatalf("SetBackupDefaults: %v", err)
	}
	svc.SetBackupCredentials(testBackupCredentials(t))
}

type stubMinter struct{}

func (stubMinter) Mint(_ context.Context, _ objectcreds.Parent, scope objectcreds.Scope) (objectcreds.Credentials, error) {
	return objectcreds.Credentials{AccessKeyID: "tmp", SecretAccessKey: "tmp", SessionToken: "tmp-" + scope.Prefix, ExpiresAt: time.Now().Add(scope.TTL)}, nil
}

type emptyStore struct{}

func (emptyStore) ListKeys(context.Context, string, string, string, int32) ([]string, string, error) {
	return nil, "", nil
}
func (emptyStore) DeleteKeys(context.Context, string, []string) error { return nil }

func testBackupCredentials(t *testing.T) *service.BackupCredentialIssuer {
	t.Helper()
	issuer, err := service.NewBackupCredentialIssuer(service.BackupCredentialIssuerConfig{
		Minter:    stubMinter{},
		OpenStore: func(context.Context, *domain.S3Credentials) (service.ObjectDeleter, error) { return emptyStore{}, nil },
		TTL:       12 * time.Hour,
		SourceTTL: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewBackupCredentialIssuer: %v", err)
	}
	return issuer
}
