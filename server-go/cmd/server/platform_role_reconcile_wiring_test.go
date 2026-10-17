package main

import "testing"

// The reconcile leads on a key no other leader-elected job takes.
func TestPlatformRoleReconcileAdvisoryKeyIsDistinct(t *testing.T) {
	others := []int64{
		appDomainSweepLockID, appImageWatchLockID, appRolloutSweepLockID, backupCredentialRenewLockID,
		backupSchedulerLockID, deletionSweepLockID, documentDBGatewayWatchLockID, functionCronLockID,
		idlePauseLockID, restoreSweepLockID, roleCertRenewLockID, storageReapLockID,
	}
	for _, key := range others {
		if key == platformRoleReconcileLockID {
			t.Fatalf("advisory key %d is shared", key)
		}
	}
	if platformRoleReconcileLockID <= 0 {
		t.Errorf("advisory key must be positive, got %d", platformRoleReconcileLockID)
	}
}
