package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestBackupCredentialExpiryIsExportedPerProject(t *testing.T) {
	expires := time.Unix(1_800_000_000, 0)
	SetBackupCredentialsExpiry("metrics-test-proj", expires)
	if got := testutil.ToFloat64(backupCredentialsExpiry.WithLabelValues("metrics-test-proj")); got != float64(expires.Unix()) {
		t.Errorf("expiry gauge = %v, want %d", got, expires.Unix())
	}
	ForgetBackupCredentialsExpiry("metrics-test-proj")
	if n := testutil.CollectAndCount(backupCredentialsExpiry, "excalibase_backup_credentials_expiry_timestamp_seconds"); n != 0 {
		t.Errorf("a forgotten project still exports %d series", n)
	}
}

func TestBackupCredentialRenewalsAreCountedByResult(t *testing.T) {
	failedBefore := testutil.ToFloat64(backupCredentialRenewals.WithLabelValues("failed"))
	renewedBefore := testutil.ToFloat64(backupCredentialRenewals.WithLabelValues("renewed"))
	CountBackupCredentialRenewal(false)
	CountBackupCredentialRenewal(true)
	CountBackupCredentialRenewal(true)
	if got := testutil.ToFloat64(backupCredentialRenewals.WithLabelValues("failed")) - failedBefore; got != 1 {
		t.Errorf("failed renewals += %v, want 1", got)
	}
	if got := testutil.ToFloat64(backupCredentialRenewals.WithLabelValues("renewed")) - renewedBefore; got != 2 {
		t.Errorf("renewals += %v, want 2", got)
	}
}
