//go:build integration

package projectdb

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

// The pg_hba a tenant cluster is rendered with, loaded into a real Postgres
// with CNPG's catch-all after it, proves what the public port admits (EXC-410).

type testCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	keyPEM  []byte
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tenant-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return testCA{cert: cert, key: key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})}
}

// serverPair is the database's certificate for "localhost", signed by the CA.
func (ca testCA) serverPair(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames:  []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func renderedHBA(t *testing.T) string {
	t.Helper()
	cluster := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: "proj-cert0001", Namespace: "org-proj-cert0001", DatabaseName: "app",
		Tier: config.TierConfig{Instances: 1, StorageSize: "1Gi", Memory: "1Gi", CPU: "1"},
	})
	lines, _, err := unstructured.NestedStringSlice(cluster.Object, "spec", "postgresql", "pg_hba")
	if err != nil || len(lines) == 0 {
		t.Fatalf("rendered pg_hba: %v", err)
	}
	// CNPG's own lines around ours: local peer first, the password catch-all last.
	return "local all all trust\n" + strings.Join(lines, "\n") + "\nhost all all all scram-sha-256\n"
}

const initScript = `#!/bin/sh
set -e
cp /tls/server.crt /tls/server.key /tls/ca.crt "$PGDATA/"
chmod 600 "$PGDATA/server.key"
cp /tls/pg_hba.conf "$PGDATA/pg_hba.conf"
cat >> "$PGDATA/postgresql.conf" <<EOF
ssl = on
ssl_cert_file = 'server.crt'
ssl_key_file = 'server.key'
ssl_ca_file = 'ca.crt'
EOF
psql -v ON_ERROR_STOP=1 -U postgres -d app <<EOF
CREATE ROLE excalibase_app LOGIN PASSWORD 'app-pw';
CREATE ROLE auth_admin LOGIN PASSWORD 'auth-pw';
CREATE ROLE cdc_watcher LOGIN REPLICATION PASSWORD 'watcher-pw';
CREATE ROLE app LOGIN PASSWORD 'customer-pw';
EOF
`

type certPostgres struct {
	host, port string
	ca         testCA
}

func startCertPostgres(t *testing.T) certPostgres {
	t.Helper()
	ctx := context.Background()
	ca := newTestCA(t)
	serverCert, serverKey := ca.serverPair(t)
	file := func(name string, content []byte, mode int64) testcontainers.ContainerFile {
		return testcontainers.ContainerFile{Reader: strings.NewReader(string(content)), ContainerFilePath: name, FileMode: mode}
	}
	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("app"), postgres.WithUsername("postgres"), postgres.WithPassword("super-pw"),
		testcontainers.WithFiles(
			file("/tls/server.crt", serverCert, 0o644),
			file("/tls/server.key", serverKey, 0o644),
			file("/tls/ca.crt", ca.certPEM, 0o644),
			file("/tls/pg_hba.conf", []byte(renderedHBA(t)), 0o644),
			file("/docker-entrypoint-initdb.d/tls.sh", []byte(initScript), 0o755),
		),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	port, err := pg.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return certPostgres{host: "localhost", port: port.Port(), ca: ca}
}

func (p certPostgres) record(t *testing.T, role string) map[string]string {
	t.Helper()
	material, err := tenantcert.Issue(tenantcert.CA{CertPEM: p.ca.certPEM, KeyPEM: p.ca.keyPEM}, role, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return material.AddTo(map[string]string{"host": p.host, "port": p.port, "database": "app", "username": role, "password": "unused"})
}

func currentUser(dsn string) (string, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var user string
	err = db.QueryRow("SELECT current_user").Scan(&user)
	return user, err
}

func (p certPostgres) passwordDSN(user, password, sslmode string) string {
	target := url.URL{Scheme: "postgres", User: url.UserPassword(user, password), Host: p.host + ":" + p.port,
		Path: "app", RawQuery: "sslmode=" + sslmode}
	return target.String()
}

func TestAPlatformRoleIsAdmittedOnlyWithItsCertificate(t *testing.T) {
	p := startCertPostgres(t)
	passwords := map[string]string{"excalibase_app": "app-pw", "auth_admin": "auth-pw", "cdc_watcher": "watcher-pw"}

	for role, password := range passwords {
		for _, sslmode := range []string{"require", "disable"} {
			if user, err := currentUser(p.passwordDSN(role, password, sslmode)); err == nil {
				t.Errorf("%s logged in with its correct password (sslmode=%s) as %s", role, sslmode, user)
			}
		}
		dsn, err := DSNFor(p.record(t, role), Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(dsn, "sslmode='verify-full'") {
			t.Fatalf("%s DSN is not verify-full", role)
		}
		user, err := currentUser(dsn)
		if err != nil || user != role {
			t.Errorf("%s with its certificate: user=%q err=%v", role, user, err)
		}
	}
}

func TestACertificateForAnotherRoleIsRefused(t *testing.T) {
	p := startCertPostgres(t)
	borrowed := p.record(t, "auth_admin")
	borrowed["username"] = "excalibase_app"
	dsn, err := DSNFor(borrowed, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if user, err := currentUser(dsn); err == nil {
		t.Fatalf("auth_admin's certificate logged in as %s", user)
	}
}

func TestTheCustomerRoleStillLogsInWithItsPasswordOverTLS(t *testing.T) {
	p := startCertPostgres(t)
	if user, err := currentUser(p.passwordDSN("app", "customer-pw", "require")); err != nil || user != "app" {
		t.Fatalf("customer over TLS: user=%q err=%v", user, err)
	}
	if _, err := currentUser(p.passwordDSN("app", "customer-pw", "disable")); err == nil {
		t.Fatal("customer plaintext admitted while TLS is required")
	}
}
