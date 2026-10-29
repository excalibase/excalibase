// Package appregistrytest builds an internal registry for other packages' tests.
package appregistrytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/appregistry"
)

// Host is the internal registry host these tests use.
const Host = "registry.excalibase.internal"

// KeyPEM is a fresh EC P-256 signing key.
func KeyPEM(t testing.TB) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// New is an internal registry with a fresh key, so each test's pull passwords are its own.
func New(t testing.TB) *appregistry.Internal {
	t.Helper()
	issuer, err := appregistry.NewIssuer(KeyPEM(t), Host)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := appregistry.NewInternal(Host, issuer)
	if err != nil {
		t.Fatal(err)
	}
	return internal
}
