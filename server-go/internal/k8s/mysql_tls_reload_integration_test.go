//go:build integration

package k8s

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"io"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/testutil/testca"
)

// EXC-589: mysqld runs with the settings the cluster's ConfigMap carries and
// the files cert-manager writes. A renewed certificate is picked up by
// ALTER INSTANCE RELOAD TLS, which MySQL refuses when it cannot chain the
// certificate to a CA it was given.
func TestIntegration_MySQLReloadsARenewedCertificate(t *testing.T) {
	ctx := context.Background()
	ca := testca.New(t, "proj MySQL CA")
	first, renewed := ca.Issue(t, "localhost"), ca.Issue(t, "localhost")

	flags := make([]string, 0, len(mysqlTLSSettings()))
	for key, value := range mysqlTLSSettings() {
		flags = append(flags, "--"+key+"="+value)
	}
	slices.Sort(flags)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "mysql:8.4",
			Env:          map[string]string{"MYSQL_ROOT_PASSWORD": "root-pw"},
			ExposedPorts: []string{"3306/tcp"},
			Files:        certManagerFiles(ca.PEM, first),
			Cmd:          flags,
			WaitingFor:   wait.ForLog("port: 3306  MySQL Community Server").WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start mysql: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	port, err := container.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}
	addr := "localhost:" + port.Port()

	open, openSerial := verifiedPool(t, ca, addr)
	session, err := open.Conn(ctx)
	if err != nil {
		t.Fatalf("verified connection: %v", err)
	}
	defer session.Close()
	if openSerial().Cmp(first.Cert.SerialNumber) != 0 {
		t.Fatalf("served serial %v, want the first certificate's", openSerial())
	}

	for _, file := range certManagerFiles(ca.PEM, renewed) {
		content, _ := io.ReadAll(file.Reader)
		if err := container.CopyToContainer(ctx, content, file.ContainerFilePath, file.FileMode); err != nil {
			t.Fatal(err)
		}
	}
	code, output, err := container.Exec(ctx, []string{"mysql", "-uroot", "-proot-pw", "-e", "ALTER INSTANCE RELOAD TLS"})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		message, _ := io.ReadAll(output)
		t.Fatalf("ALTER INSTANCE RELOAD TLS exited %d: %s", code, message)
	}

	fresh, freshSerial := verifiedPool(t, ca, addr)
	if err := fresh.PingContext(ctx); err != nil {
		t.Fatalf("verified connection after the reload: %v", err)
	}
	if freshSerial().Cmp(renewed.Cert.SerialNumber) != 0 {
		t.Errorf("served serial %v after the reload, want the renewed certificate's", freshSerial())
	}
	if err := session.PingContext(ctx); err != nil {
		t.Errorf("a session opened before the reload dropped: %v", err)
	}
}

// certManagerFiles lays out a certificate secret as its mount presents it.
func certManagerFiles(caPEM string, server testca.Server) []testcontainers.ContainerFile {
	return []testcontainers.ContainerFile{
		{Reader: strings.NewReader(caPEM), ContainerFilePath: MySQLTLSDir + "/ca.crt", FileMode: 0o644},
		{Reader: strings.NewReader(server.CertPEM), ContainerFilePath: MySQLTLSDir + "/tls.crt", FileMode: 0o644},
		{Reader: strings.NewReader(server.KeyPEM), ContainerFilePath: MySQLTLSDir + "/tls.key", FileMode: 0o644},
	}
}

// verifiedPool opens a pool that verifies the server against ca and reports
// the serial of the certificate the server last presented.
func verifiedPool(t *testing.T, ca testca.CA, addr string) (*sql.DB, func() *big.Int) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	var served *big.Int
	name := strings.ReplaceAll(t.Name(), "/", "_") + "-" + big.NewInt(time.Now().UnixNano()).String()
	if err := mysql.RegisterTLSConfig(name, &tls.Config{
		RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12,
		VerifyConnection: func(state tls.ConnectionState) error {
			served = state.PeerCertificates[0].SerialNumber
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	config := mysql.NewConfig()
	config.Net, config.Addr, config.User, config.Passwd, config.TLSConfig = "tcp", addr, "root", "root-pw", name
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("verified connection: %v", err)
	}
	return db, func() *big.Int { return served }
}
