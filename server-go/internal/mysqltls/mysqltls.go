// Package mysqltls is how the platform's own MySQL clients check a project's
// server (EXC-589): TLS on every connection, the certificate verified against
// the project's CA and the name the vault record dials.
package mysqltls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"time"
)

// ErrNoServerCA is a record or server that hands out no CA to verify with.
var ErrNoServerCA = errors.New("no MySQL server CA to verify the server with")

// ClientConfig trusts only rootPEM (one CA or a bundle) and requires the
// server certificate to name serverName. The address dialled may differ (a
// port-forward); the name checked never does.
func ClientConfig(rootPEM, serverName string) (*tls.Config, error) {
	roots := x509.NewCertPool()
	if rootPEM == "" || !roots.AppendCertsFromPEM([]byte(rootPEM)) {
		return nil, ErrNoServerCA
	}
	if serverName == "" {
		return nil, errors.New("no MySQL server name to verify")
	}
	return &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12}, nil
}

// CertificateNotAfter is when the first certificate in certPEM stops being
// valid.
func CertificateNotAfter(certPEM string) (time.Time, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, errors.New("no certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}
