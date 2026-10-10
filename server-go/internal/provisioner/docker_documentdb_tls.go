package provisioner

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// gatewayTLSValidity is how long the gateway's CA and certificate last. A
// single host has no operator renewing them; a restore or a new project issues
// fresh ones (see ADR 0039).
const gatewayTLSValidity = 10 * 365 * 24 * time.Hour

// gatewayTLS is a project's own CA and the gateway's certificate from it.
// The CA's key is discarded once the certificate is signed.
type gatewayTLS struct {
	caPEM, certPEM, keyPEM []byte
}

// newGatewayTLS issues a CA and a server certificate for the names a client
// dials: the database container's name on the platform network, and loopback
// on the host where the port is published.
func newGatewayTLS(host string, now time.Time) (gatewayTLS, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return gatewayTLS{}, fmt.Errorf("gateway CA key: %w", err)
	}
	caSerial, err := serialNumber()
	if err != nil {
		return gatewayTLS{}, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: host + " DocumentDB CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(gatewayTLSValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return gatewayTLS{}, fmt.Errorf("gateway CA: %w", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return gatewayTLS{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return gatewayTLS{}, fmt.Errorf("gateway key: %w", err)
	}
	serial, err := serialNumber()
	if err != nil {
		return gatewayTLS{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host, "localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(gatewayTLSValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		return gatewayTLS{}, fmt.Errorf("gateway certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return gatewayTLS{}, err
	}
	return gatewayTLS{
		caPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

func serialNumber() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("certificate serial: %w", err)
	}
	return serial, nil
}
