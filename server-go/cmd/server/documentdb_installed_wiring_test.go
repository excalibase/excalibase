package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// EXC-394: one setting decides DocumentDB for provisioning and restore alike.
func TestTheDocumentDBSettingReachesProvisioningAndRestore(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		provSvc := service.NewProvisioningService(nil, nil, nil)
		backups := handler.NewBackupHandler(nil)
		wireDocumentDBInstalled(config.AppConfig{DocumentDBEnabled: enabled}, provSvc, backups)
		if provSvc.DocumentDBEnabled() != enabled || backups.DocumentDBEnabled() != enabled {
			t.Errorf("enabled=%v: provisioning %v, restore %v", enabled, provSvc.DocumentDBEnabled(), backups.DocumentDBEnabled())
		}
	}
}
