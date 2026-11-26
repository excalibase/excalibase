package netmeter

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.crt")
	block := &pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeToken(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func serverPort(t *testing.T, server *httptest.Server) string {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestKubeletFetcher_ReadsSummaryWithTheServiceAccountToken(t *testing.T) {
	var gotAuth, gotPath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte(summaryJSON))
	}))
	defer server.Close()
	fetcher, err := NewKubeletFetcher(writeCA(t, server), writeToken(t, "tok-1"), serverPort(t, server))
	if err != nil {
		t.Fatal(err)
	}
	pods, err := fetcher.Summary(context.Background(), "127.0.0.1")
	if err != nil || len(pods) != 2 {
		t.Fatalf("Summary: %v %+v", err, pods)
	}
	if gotAuth != "Bearer tok-1" || gotPath != "/stats/summary" {
		t.Errorf("request: auth %q path %q", gotAuth, gotPath)
	}
}

func TestKubeletFetcher_RereadsTheTokenEachCall(t *testing.T) {
	var gotAuth string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(summaryJSON))
	}))
	defer server.Close()
	tokenPath := writeToken(t, "old")
	fetcher, err := NewKubeletFetcher(writeCA(t, server), tokenPath, serverPort(t, server))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fetcher.Summary(context.Background(), "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer rotated" {
		t.Errorf("projected tokens rotate; auth = %q", gotAuth)
	}
}

func TestKubeletFetcher_RefusesAServerTheCADoesNotSign(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(summaryJSON))
	}))
	defer server.Close()
	fetcher, err := NewKubeletFetcher(writeUnrelatedCA(t), writeToken(t, "t"), serverPort(t, server))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fetcher.Summary(context.Background(), "127.0.0.1"); err == nil {
		t.Fatal("a certificate the CA did not sign must fail verification; there is no skip-verify")
	}
}

// writeUnrelatedCA writes a self-signed CA that signed nothing the test server uses.
func writeUnrelatedCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unrelated"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "other.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKubeletFetcher_ErrorsOnNonOKAnswer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()
	fetcher, _ := NewKubeletFetcher(writeCA(t, server), writeToken(t, "t"), serverPort(t, server))
	if _, err := fetcher.Summary(context.Background(), "127.0.0.1"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want a 403 error, got %v", err)
	}
}

func TestKubeletFetcher_RefusesAMissingOrEmptyCA(t *testing.T) {
	if _, err := NewKubeletFetcher(filepath.Join(t.TempDir(), "none"), "tok", "10250"); err == nil {
		t.Error("a missing CA file is an error at start, not a silent insecure client")
	}
	empty := filepath.Join(t.TempDir(), "empty.crt")
	if err := os.WriteFile(empty, []byte("not a cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewKubeletFetcher(empty, "tok", "10250"); err == nil {
		t.Error("a CA file with no certificate is an error")
	}
}
