package handler

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/service"
)

// withBackupTarget gives the service the backup target every tier requires.
func withBackupTarget(t *testing.T, svc *service.ProvisioningService) {
	t.Helper()
	if err := svc.SetBackupDefaults(&service.BackupDefaults{
		AccessKeyID: "k", SecretAccessKey: "s", Endpoint: "https://backups.example.test", Bucket: "backups",
	}); err != nil {
		t.Fatalf("SetBackupDefaults: %v", err)
	}
}
