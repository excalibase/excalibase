package tenantcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// cnpgStyleCA mints a CA the way CNPG does: EC P-256, key as "EC PRIVATE KEY".
func cnpgStyleCA(t *testing.T) CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "proj-a-postgres", OrganizationalUnit: []string{"proj-ns"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(90 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return CA{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
}

func parseCert(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatal("no PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// Postgres "cert" auth matches the certificate CN to the role, so the CN is the
// role and the chain must verify against the cluster CA as a client cert.
func TestAnIssuedCertificateNamesTheRoleAndChainsToTheClusterCA(t *testing.T) {
	ca := cnpgStyleCA(t)
	material, err := Issue(ca, "excalibase_app", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cert := parseCert(t, material.Cert)
	if cert.Subject.CommonName != "excalibase_app" {
		t.Errorf("CN = %q", cert.Subject.CommonName)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.CertPEM)
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err != nil {
		t.Errorf("certificate does not verify as a client of the cluster CA: %v", err)
	}
	if material.RootCert != string(ca.CertPEM) {
		t.Error("the root handed to clients is not the cluster CA")
	}
	if got := cert.NotAfter.Sub(now); got < Validity-time.Hour || got > Validity+time.Hour {
		t.Errorf("validity %v, want about %v", got, Validity)
	}
	if cert.IsCA {
		t.Error("a client certificate must not be a CA")
	}
}

func TestTheKeyIsPKCS8AndMatchesTheCertificate(t *testing.T) {
	material, err := Issue(cnpgStyleCA(t), "auth_admin", now)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(material.Key))
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatalf("key is not a PKCS#8 PEM block: %v", block)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	public := key.(*ecdsa.PrivateKey).Public().(*ecdsa.PublicKey)
	if !public.Equal(parseCert(t, material.Cert).PublicKey) {
		t.Error("key does not match the certificate")
	}
}

func TestEachIssueHasItsOwnKey(t *testing.T) {
	ca := cnpgStyleCA(t)
	first, _ := Issue(ca, "cdc_watcher", now)
	second, _ := Issue(ca, "cdc_watcher", now)
	if first.Key == second.Key {
		t.Error("two issues share a private key")
	}
}

func TestACAWithoutAKeyIsRefused(t *testing.T) {
	ca := cnpgStyleCA(t)
	ca.KeyPEM = nil
	if _, err := Issue(ca, "excalibase_app", now); !errors.Is(err, ErrInvalidCA) {
		t.Fatalf("err = %v, want ErrInvalidCA", err)
	}
}

func TestAnEmptyRoleIsRefused(t *testing.T) {
	if _, err := Issue(cnpgStyleCA(t), "", now); err == nil {
		t.Fatal("a certificate for no role was issued")
	}
}

func TestMaterialRoundTripsThroughAVaultRecord(t *testing.T) {
	material, _ := Issue(cnpgStyleCA(t), "excalibase_app", now)
	record := map[string]string{"host": "h", "password": "p"}
	withCert := material.AddTo(record)
	if _, ok := record[FieldCert]; ok {
		t.Error("AddTo changed the record it was given")
	}
	got, err := FromRecord(withCert)
	if err != nil {
		t.Fatal(err)
	}
	if got != material || withCert["host"] != "h" {
		t.Errorf("round trip lost data")
	}
}

func TestARecordWithoutACertificateIsReportedMissing(t *testing.T) {
	material, _ := Issue(cnpgStyleCA(t), "excalibase_app", now)
	for _, field := range []string{FieldCert, FieldKey, FieldRootCert} {
		record := material.AddTo(map[string]string{})
		delete(record, field)
		if _, err := FromRecord(record); !errors.Is(err, ErrNoClientCertificate) {
			t.Errorf("without %s: err = %v", field, err)
		}
	}
}

func TestRenewalIsDueNearExpiryOrWhenTheCAChanged(t *testing.T) {
	ca := cnpgStyleCA(t)
	material, _ := Issue(ca, "excalibase_app", now)
	record := material.AddTo(map[string]string{})
	if RenewalDue(record, string(ca.CertPEM), now.Add(Validity-RenewBefore-time.Hour)) {
		t.Error("renewal due too early")
	}
	if !RenewalDue(record, string(ca.CertPEM), now.Add(Validity-RenewBefore+time.Hour)) {
		t.Error("renewal not due inside the window")
	}
	renewedCA := cnpgStyleCA(t)
	if !RenewalDue(record, string(renewedCA.CertPEM), now) {
		t.Error("a changed cluster CA certificate must be republished")
	}
	if !RenewalDue(map[string]string{"password": "p"}, string(ca.CertPEM), now) {
		t.Error("a record with no certificate must be issued one")
	}
}

func TestACAKeptAsPKCS8IsAccepted(t *testing.T) {
	ca := cnpgStyleCA(t)
	block, _ := pem.Decode(ca.KeyPEM)
	key, _ := x509.ParseECPrivateKey(block.Bytes)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	ca.KeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := Issue(ca, "excalibase_app", now); err != nil {
		t.Fatalf("Issue: %v", err)
	}
}

func TestAnUnusableCAIsRefused(t *testing.T) {
	ca := cnpgStyleCA(t)
	leaf, _ := Issue(ca, "excalibase_app", now)
	cases := map[string]CA{
		"not a CA":      {CertPEM: []byte(leaf.Cert), KeyPEM: []byte(leaf.Key)},
		"garbled cert":  {CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}), KeyPEM: ca.KeyPEM},
		"garbled key":   {CertPEM: ca.CertPEM, KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("x")})},
		"garbled pkcs8": {CertPEM: ca.CertPEM, KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})},
	}
	for name, bad := range cases {
		if _, err := Issue(bad, "excalibase_app", now); !errors.Is(err, ErrInvalidCA) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestAnUnreadableCertificateIsRenewed(t *testing.T) {
	ca := cnpgStyleCA(t)
	record := Material{Cert: "not PEM", Key: "k", RootCert: string(ca.CertPEM)}.AddTo(nil)
	if !RenewalDue(record, string(ca.CertPEM), now) {
		t.Error("a record whose certificate cannot be read was kept")
	}
	record[FieldCert] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}))
	if !RenewalDue(record, string(ca.CertPEM), now) {
		t.Error("a record whose certificate does not parse was kept")
	}
}

func TestIsPlatformRole(t *testing.T) {
	if !IsPlatformRole("cdc_watcher") || IsPlatformRole("app") {
		t.Error("IsPlatformRole misclassifies")
	}
}
