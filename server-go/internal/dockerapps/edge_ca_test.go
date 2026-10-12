package dockerapps

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func certificatePEM(t *testing.T, isCA bool) []byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "edge"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: isCA, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func serve(t *testing.T, status int, body []byte) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/root.crt"
}

func TestEdgeCAReadsTheEdgesRootCertificate(t *testing.T) {
	root := certificatePEM(t, true)
	got, err := EdgeCAReader(serve(t, http.StatusOK, root))(context.Background())
	if err != nil || string(got) != string(root) {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Only a CA certificate is handed to apps; anything else, or no answer, is an error.
func TestEdgeCARefusesWhatIsNotACACertificate(t *testing.T) {
	for name, url := range map[string]string{
		"not found":   serve(t, http.StatusNotFound, nil),
		"not PEM":     serve(t, http.StatusOK, []byte("<html>hello</html>")),
		"not a CA":    serve(t, http.StatusOK, certificatePEM(t, false)),
		"unreachable": "http://127.0.0.1:1/root.crt",
	} {
		if _, err := EdgeCAReader(url)(context.Background()); err == nil {
			t.Errorf("%s: read without error", name)
		}
	}
}
