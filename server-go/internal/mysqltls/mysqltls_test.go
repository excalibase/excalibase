package mysqltls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/testutil/testca"
)

// verify runs the server check crypto/tls runs in a handshake.
func verify(config *tls.Config, server testca.Server) error {
	_, err := server.Cert.Verify(x509.VerifyOptions{Roots: config.RootCAs, DNSName: config.ServerName})
	return err
}

func TestClientConfig_TrustsOnlyTheProjectCAForTheRecordedName(t *testing.T) {
	ca, other := testca.New(t, "project CA"), testca.New(t, "other CA")
	host := "moco-p-mysql-primary.org-p.svc.cluster.local"
	config, err := ClientConfig(ca.PEM, host)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion < tls.VersionTLS12 || config.InsecureSkipVerify {
		t.Errorf("config %+v", config)
	}
	if err := verify(config, ca.Issue(t, host)); err != nil {
		t.Errorf("the project's own certificate was refused: %v", err)
	}
	if verify(config, other.Issue(t, host)) == nil {
		t.Error("a certificate from another CA was accepted")
	}
	if verify(config, ca.Issue(t, "moco-q-mysql-primary.org-q.svc.cluster.local")) == nil {
		t.Error("a certificate for another name was accepted")
	}
}

func TestClientConfig_AcceptsABundle(t *testing.T) {
	old, current := testca.New(t, "old"), testca.New(t, "current")
	config, err := ClientConfig(old.PEM+current.PEM, "db")
	if err != nil {
		t.Fatal(err)
	}
	for _, ca := range []testca.CA{old, current} {
		if err := verify(config, ca.Issue(t, "db")); err != nil {
			t.Errorf("bundle member %s refused: %v", ca.Cert.Subject.CommonName, err)
		}
	}
}

func TestClientConfig_RefusesWhatIsNotACA(t *testing.T) {
	for name, rootPEM := range map[string]string{"empty": "", "garbage": "not a certificate", "public key": "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n"} {
		if _, err := ClientConfig(rootPEM, "db"); !errors.Is(err, ErrNoServerCA) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := ClientConfig(testca.New(t, "ca").PEM, ""); err == nil {
		t.Error("no name to verify was accepted")
	}
}

func TestCertificateNotAfter(t *testing.T) {
	server := testca.New(t, "expiry").Issue(t, "db")
	got, err := CertificateNotAfter(server.CertPEM)
	if err != nil || !got.Equal(server.Cert.NotAfter) {
		t.Fatalf("got %v, %v; want %v", got, err, server.Cert.NotAfter)
	}
	if _, err := CertificateNotAfter("not a certificate"); err == nil {
		t.Fatal("read an expiry from no certificate")
	}
}
