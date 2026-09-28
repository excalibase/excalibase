package k8s

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"sync"
	"time"
)

// clusterCASuffix names the Secret CNPG keeps each cluster's CA in.
const clusterCASuffix = "-postgres-ca"

var (
	mockClusterCAOnce sync.Once
	mockClusterCA     map[string][]byte
)

// MockClusterCA is the CA the mock serves for every "<cluster>-ca" Secret a
// test did not seed, shaped like CNPG's: ca.crt plus an EC ca.key.
func MockClusterCA() map[string][]byte {
	mockClusterCAOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
		template := &x509.Certificate{
			SerialNumber:          big.NewInt(1),
			Subject:               pkix.Name{CommonName: "mock-cluster-ca"},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().Add(90 * 24 * time.Hour),
			IsCA:                  true,
			BasicConstraintsValid: true,
			KeyUsage:              x509.KeyUsageCertSign,
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			panic(err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			panic(err)
		}
		mockClusterCA = map[string][]byte{
			"ca.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			"ca.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		}
	})
	return mockClusterCA
}

func isClusterCASecret(name string) bool {
	return strings.HasSuffix(name, clusterCASuffix)
}
