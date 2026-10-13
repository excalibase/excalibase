package mcpserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// localEdge is an HTTPS edge signed by its own CA, answering the host each request named.
func localEdge(t *testing.T) (*httptest.Server, []byte) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "edge local CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(2),
		DNSNames: []string{"api.example.test", "*.apps.example.test"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
		ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	edge := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Host)
	}))
	edge.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	edge.StartTLS()
	t.Cleanup(edge.Close)
	return edge, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func edgeSettings(edge *httptest.Server, root []byte) Settings {
	return Settings{Edge: &EdgeRoute{
		TLSAddress: edge.Listener.Addr().String(), Hosts: []string{"api.example.test"}, Domain: "apps.example.test",
		Root: func(context.Context) ([]byte, error) { return root, nil },
	}}
}

// EXC-608: on a single host the platform's names (here unresolvable, as *.localhost is in a container)
// are the edge's; the probes reach it there and trust its own CA.
func TestProbesReachThePlatformAndItsAppsThroughTheEdge(t *testing.T) {
	edge, root := localEdge(t)
	client, err := edgeSettings(edge, root).probeClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A proxy from the environment would carry the request past the edge's routing.
	if transport, ok := client.Transport.(*http.Transport); !ok || transport.Proxy != nil {
		t.Fatal("probes through the edge must not go through an environment proxy")
	}
	for _, address := range []string{"https://api.example.test/healthz", "https://web-abc.apps.example.test/"} {
		answer, err := client.Get(address)
		if err != nil {
			t.Fatalf("%s: %v", address, err)
		}
		body, _ := io.ReadAll(answer.Body)
		answer.Body.Close()
		if answer.StatusCode != 200 || len(body) == 0 {
			t.Fatalf("%s answered %d %q", address, answer.StatusCode, body)
		}
	}
	if probe := probeAppURL(context.Background(), client, "https://web-abc.apps.example.test"); !probe.Answered {
		t.Fatalf("app URL probe = %+v", probe)
	}
}

// Hosts that are not the platform's go where DNS says, and the edge's CA is trusted for nothing else.
func TestProbesSendOtherHostsWhereTheyResolve(t *testing.T) {
	edge, root := localEdge(t)
	client, err := edgeSettings(edge, root).probeClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get("https://elsewhere.example.test/"); err == nil {
		t.Fatal("a host outside the platform was sent to the edge")
	}
}

// No edge route (Kubernetes): the default client, the system's roots.
func TestProbesWithoutAnEdgeRouteUseTheSystem(t *testing.T) {
	client, err := Settings{}.probeClient(context.Background())
	if err != nil || client.Transport != nil {
		t.Fatalf("client transport = %v, err = %v", client.Transport, err)
	}
}
