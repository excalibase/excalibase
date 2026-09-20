package projectdb

import (
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A tenant database can be made to hang on a statement or a lock. Abandoning
// the client side is not enough — the server keeps running the statement and
// holding the connection — so every pooled connection carries its own bound.
func TestPoolDSN_CarriesStatementAndLockTimeouts(t *testing.T) {
	dsn := poolDSN(appCreds()["projects/proj_a/credentials/excalibase_app"], Overrides{},
		PoolLimits{StatementTimeout: 30 * time.Second, LockTimeout: 5 * time.Second}.withDefaults())

	for _, want := range []string{"statement_timeout=30000", "lock_timeout=5000"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn %q is missing %s", dsn, want)
		}
	}
}

func TestPoolLimits_TimeoutDefaults(t *testing.T) {
	l := PoolLimits{}.withDefaults()
	if l.StatementTimeout != defaultStatementTimeout {
		t.Errorf("StatementTimeout: got %v, want %v", l.StatementTimeout, defaultStatementTimeout)
	}
	if l.LockTimeout != defaultLockTimeout {
		t.Errorf("LockTimeout: got %v, want %v", l.LockTimeout, defaultLockTimeout)
	}
}

// The opener is the one way the scheduler reaches a tenant, so an opener
// built without explicit limits must still hand out bounded connections.
func TestNewOpener_AppliesTimeoutDefaults(t *testing.T) {
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"}),
		fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()

	if o.limits.StatementTimeout <= 0 || o.limits.LockTimeout <= 0 {
		t.Fatalf("limits: got statement=%v lock=%v, want both bounded",
			o.limits.StatementTimeout, o.limits.LockTimeout)
	}
}
