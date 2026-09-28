package handler

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/projectdb"
)

func TestRealtimeDialsTheProjectWithTLSRequired(t *testing.T) {
	dsn, err := realtimeDSN(map[string]string{
		"host": "p-postgres-rw.ns.svc.cluster.local", "port": "5432",
		"username": "excalibase_app", "password": "pw", "database": "app",
		"sslcert": "CERT-PEM", "sslkey": "KEY-PEM", "sslrootcert": "CA-PEM",
	}, projectdb.Overrides{})
	if err != nil {
		t.Fatalf("realtimeDSN: %v", err)
	}
	if !strings.Contains(dsn, "sslmode='verify-full'") || !strings.Contains(dsn, "sslcert='CERT-PEM'") {
		t.Fatalf("realtime connects without TLS: %s", strings.Replace(dsn, "pw", "***", 1))
	}
}
