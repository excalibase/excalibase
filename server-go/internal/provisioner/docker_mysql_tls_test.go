package provisioner

import (
	"github.com/excalibase/provisioning-poc/internal/testutil/testca"

	"archive/tar"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"strings"
	"testing"
)

// mysqlTLSFiles reads the material written into the container before it starts.
func mysqlTLSFiles(t *testing.T, engine *fakeEngine, container string) map[string]string {
	t.Helper()
	engine.mu.Lock()
	body := engine.copied[container+":/tmp"]
	engine.mu.Unlock()
	files := map[string]string{}
	reader := tar.NewReader(bytes.NewReader(body))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			content, _ := io.ReadAll(reader)
			files[header.Name] = string(content)
			if header.Uid != MySQLImageOwner.UID || header.Mode != 0o600 {
				t.Errorf("%s owned by %d mode %o", header.Name, header.Uid, header.Mode)
			}
		}
	}
}

// EXC-589: a single host has no cert-manager, so the platform issues the
// project's CA and server certificate itself (like the DocumentDB gateway,
// ADR 0039). They are written in before mysqld first starts, kept on the data
// volume so a recreated container keeps them, and mysqld presents them. The
// certificate names the container, never the engine's id for it.
func TestDockerMySQLPresentsACertificateFromTheProjectsCA(t *testing.T) {
	engine := newFakeEngine()
	engine.engineIDs = true
	provisionDockerMySQL(t, engine)
	container := "excalibase-p1-mysql"
	if copied, started := engine.indexOf("copy:"+container+":/tmp"), engine.indexOf("start:"); copied < 0 || started < 0 || copied > started {
		t.Fatalf("material copied at %d, container started at %d: %v", copied, started, engine.calls)
	}
	files := mysqlTLSFiles(t, engine, container)
	block, _ := pem.Decode([]byte(files["excalibase-mysql-tls/tls.crt"]))
	if block == nil {
		t.Fatalf("no certificate in %v", files)
	}
	cert, _ := x509.ParseCertificate(block.Bytes)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(files["excalibase-mysql-tls/ca.crt"]))
	for _, name := range []string{container, "localhost"} {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !strings.Contains(files["excalibase-mysql-tls/tls.key"], "PRIVATE KEY") {
		t.Error("no key")
	}

	script := engine.specs[container].Cmd[0]
	for _, want := range []string{
		"--ssl-cert=" + MySQLTLSDir + "/tls.crt", "--ssl-key=" + MySQLTLSDir + "/tls.key",
		"[ ! -f " + MySQLTLSDir + "/tls.crt ]", "cp /tmp/excalibase-mysql-tls/* " + MySQLTLSDir + "/",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("start script lacks %q:\n%s", want, script)
		}
	}
	if strings.Index(script, "--initialize-insecure") > strings.Index(script, "mkdir -p "+MySQLTLSDir) {
		t.Error("the material lands in the datadir before mysqld initialises it, which needs it empty")
	}
}

func TestDockerMySQLServerCA(t *testing.T) {
	engine := newFakeEngine()
	provisionDockerMySQL(t, engine)
	files := mysqlTLSFiles(t, engine, "excalibase-p1-mysql")
	engine.mu.Lock()
	engine.copied["excalibase-p1-mysql:"+MySQLTLSDir] = tarOfFiles(t, map[string]string{"ca.crt": files["excalibase-mysql-tls/ca.crt"]})
	engine.mu.Unlock()
	got, err := newTestDockerMySQL(engine).ServerCA(context.Background(), "excalibase-p1-mysql", "p1")
	if err != nil || got != files["excalibase-mysql-tls/ca.crt"] {
		t.Fatalf("got %q, %v", got, err)
	}
}

func tarOfFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for name, body := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write([]byte(body))
	}
	_ = writer.Close()
	return buffer.Bytes()
}

func TestDockerMySQLServerCertificateExpiry(t *testing.T) {
	engine := newFakeEngine()
	provisionDockerMySQL(t, engine)
	server := testca.New(t, "expiry").Issue(t, "db")
	engine.mu.Lock()
	engine.copied["excalibase-p1-mysql:"+MySQLTLSDir] = tarOfFiles(t, map[string]string{"tls.crt": server.CertPEM})
	engine.mu.Unlock()
	got, err := newTestDockerMySQL(engine).ServerCertificateExpiry(context.Background(), "excalibase-p1-mysql", "p1")
	if err != nil || !got.Equal(server.Cert.NotAfter) {
		t.Fatalf("got %v, %v", got, err)
	}
}
