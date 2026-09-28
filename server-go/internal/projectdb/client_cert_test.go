package projectdb

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

func certRecord() map[string]string {
	return maps.Clone(appCreds()["projects/proj_a/credentials/excalibase_app"])
}

// excalibase_app logs in with its certificate (EXC-410); verify-full against
// the cluster CA, and the PEM travels inline rather than through files.
func TestDSN_APlatformRoleLogsInWithItsCertificate(t *testing.T) {
	dsn, err := DSNFor(certRecord(), Overrides{})
	if err != nil {
		t.Fatalf("DSNFor: %v", err)
	}
	for _, want := range []string{"sslmode='verify-full'", "sslinline='true'",
		"sslcert='CERT-PEM'", "sslkey='KEY-PEM'", "sslrootcert='CA-PEM'"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN lacks %s", want)
		}
	}
}

// A port-forward's host is not in the server certificate; the CA still is.
func TestDSN_AHostOverrideVerifiesTheCAButNotTheName(t *testing.T) {
	dsn, err := DSNFor(certRecord(), Overrides{Host: "127.0.0.1", SSLMode: "require"})
	if err != nil {
		t.Fatalf("DSNFor: %v", err)
	}
	if !strings.Contains(dsn, "sslmode='verify-ca'") || !strings.Contains(dsn, "sslcert='CERT-PEM'") {
		t.Errorf("override DSN does not verify the CA with the certificate")
	}
}

func withoutCertificate() map[string]string {
	record := appCreds()["projects/proj_a/credentials/excalibase_app"]
	delete(record, tenantcert.FieldCert)
	return record
}

func TestDSN_APlatformRoleWithoutACertificateIsRefused(t *testing.T) {
	record := withoutCertificate()
	if _, err := DSNFor(record, Overrides{}); !errors.Is(err, tenantcert.ErrNoClientCertificate) {
		t.Fatalf("err = %v, want ErrNoClientCertificate", err)
	}
}

// The docker all-in-one runs tenants without TLS (and without our pg_hba).
func TestDSN_DisabledTLSUsesThePassword(t *testing.T) {
	record := withoutCertificate()
	dsn, err := DSNFor(record, Overrides{Host: "tenant-db", SSLMode: "disable"})
	if err != nil {
		t.Fatalf("DSNFor: %v", err)
	}
	if strings.Contains(dsn, "sslcert") || !strings.Contains(dsn, "password='pw'") {
		t.Errorf("disabled TLS DSN = %q", dsn)
	}
}

func TestDSN_TheCustomerRoleKeepsItsPassword(t *testing.T) {
	record := map[string]string{"host": "db.internal", "port": "5432", "username": "app", "password": "pw", "database": "app"}
	dsn, err := DSNFor(record, Overrides{})
	if err != nil {
		t.Fatalf("DSNFor: %v", err)
	}
	if strings.Contains(dsn, "sslcert") || !strings.Contains(dsn, "sslmode='require'") {
		t.Errorf("customer DSN = %q", dsn)
	}
}

// A pool is opened with the certificate filed at the time. A renewed one is
// only picked up by reopening, so a pool is not kept past MaxPoolAge.
func TestOpen_ReopensAPoolOlderThanItsMaximumAge(t *testing.T) {
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"}),
		fakeVault{data: appCreds()}, Overrides{}, PoolLimits{MaxPoolAge: time.Hour})
	defer o.Close()
	clock := time.Now()
	o.now = func() time.Time { return clock }

	first, err := o.Open(context.Background(), "proj_a")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(59 * time.Minute)
	if again, _ := o.Open(context.Background(), "proj_a"); again != first {
		t.Fatal("a pool inside its age was replaced")
	}
	clock = clock.Add(2 * time.Minute)
	renewed, err := o.Open(context.Background(), "proj_a")
	if err != nil {
		t.Fatal(err)
	}
	if renewed == first {
		t.Error("a pool past its maximum age was handed out again")
	}
	if o.CachedPools() != 1 {
		t.Errorf("cached pools = %d", o.CachedPools())
	}
}

func TestPoolLimits_MaxPoolAgeDefaultsToAnHour(t *testing.T) {
	if got := (PoolLimits{}).withDefaults().MaxPoolAge; got != time.Hour {
		t.Errorf("MaxPoolAge default = %v", got)
	}
}
