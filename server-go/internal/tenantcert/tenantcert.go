// Package tenantcert issues the client certificates the platform's own roles
// log in to a tenant database with (EXC-410). pg_hba admits those roles only
// by "cert", so the certificate is the credential: its CN is the role, and it
// is signed by the cluster's client CA, the same CA CNPG signs its own
// streaming_replica certificate with.
package tenantcert

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"time"
)

// Vault record fields, named after the libpq parameters they feed. Each holds
// PEM text, not a path.
const (
	FieldCert     = "sslcert"
	FieldKey      = "sslkey"
	FieldRootCert = "sslrootcert"
)

// PlatformRoles are the roles the platform itself logs in as. pg_hba refuses
// each of them a password over the network.
var PlatformRoles = []string{"excalibase_app", "auth_admin", "cdc_watcher"}

// IsPlatformRole reports whether username is one of PlatformRoles.
func IsPlatformRole(username string) bool {
	return slices.Contains(PlatformRoles, username)
}

const (
	// Validity matches the lifetime CNPG gives its own certificates.
	Validity = 90 * 24 * time.Hour
	// RenewBefore leaves consumers weeks to pick up a renewed record.
	RenewBefore = 30 * 24 * time.Hour
	// backdate absorbs clock skew between the platform and the database.
	backdate = 5 * time.Minute
)

var (
	ErrInvalidCA           = errors.New("cluster client CA is missing or unreadable")
	ErrNoClientCertificate = errors.New("credential record carries no client certificate")
)

// CA is the cluster's client CA as CNPG stores it: ca.crt and ca.key.
type CA struct {
	CertPEM []byte
	KeyPEM  []byte
}

// Material is one role's certificate, key and the CA its server presents.
type Material struct {
	Cert     string
	Key      string
	RootCert string
}

// Issue signs a fresh client certificate for role with its own new key.
func Issue(ca CA, role string, now time.Time) (Material, error) {
	if role == "" {
		return Material{}, errors.New("a client certificate needs a role")
	}
	caCert, caKey, err := parseCA(ca)
	if err != nil {
		return Material{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Material{}, fmt.Errorf("generate client key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Material{}, fmt.Errorf("generate serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: role},
		NotBefore:    now.Add(-backdate),
		NotAfter:     now.Add(Validity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return Material{}, fmt.Errorf("sign client certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Material{}, fmt.Errorf("encode client key: %w", err)
	}
	return Material{
		Cert:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		Key:      string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		RootCert: string(ca.CertPEM),
	}, nil
}

func parseCA(ca CA) (*x509.Certificate, crypto.Signer, error) {
	certBlock, _ := pem.Decode(ca.CertPEM)
	keyBlock, _ := pem.Decode(ca.KeyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, nil, ErrInvalidCA
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil || !cert.IsCA {
		return nil, nil, ErrInvalidCA
	}
	key, err := parsePrivateKey(keyBlock)
	if err != nil {
		return nil, nil, ErrInvalidCA
	}
	return cert, key, nil
}

func parsePrivateKey(block *pem.Block) (crypto.Signer, error) {
	if block.Type == "EC PRIVATE KEY" {
		return x509.ParseECPrivateKey(block.Bytes)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, ErrInvalidCA
	}
	return signer, nil
}

// AddTo returns a copy of record carrying the material.
func (m Material) AddTo(record map[string]string) map[string]string {
	withCert := maps.Clone(record)
	if withCert == nil {
		withCert = map[string]string{}
	}
	withCert[FieldCert] = m.Cert
	withCert[FieldKey] = m.Key
	withCert[FieldRootCert] = m.RootCert
	return withCert
}

// FromRecord reads the material out of a vault record; all three are required.
func FromRecord(record map[string]string) (Material, error) {
	material := Material{Cert: record[FieldCert], Key: record[FieldKey], RootCert: record[FieldRootCert]}
	if material.Cert == "" || material.Key == "" || material.RootCert == "" {
		return Material{}, ErrNoClientCertificate
	}
	return material, nil
}

// RenewalDue reports whether a record needs a new certificate: it has none,
// it is inside the renewal window, or the cluster CA certificate it hands out
// is no longer the one the server presents.
func RenewalDue(record map[string]string, currentRootCert string, now time.Time) bool {
	material, err := FromRecord(record)
	if err != nil {
		return true
	}
	if material.RootCert != currentRootCert {
		return true
	}
	block, _ := pem.Decode([]byte(material.Cert))
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	return !now.Add(RenewBefore).Before(cert.NotAfter)
}
