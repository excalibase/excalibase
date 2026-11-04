// Package appregistrytest builds an internal registry for other packages' tests.
package appregistrytest

import (
	"archive/tar"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"strconv"
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
	guard, err := appregistry.NewInternal(Host, nil)
	if err != nil {
		t.Fatal(err)
	}
	return guard.Issuing(issuer, Unrotated{})
}

// Unrotated is every project at generation 0.
type Unrotated struct{}

func (Unrotated) PullGeneration(string) (int, error) { return 0, nil }

// Archive is a build's image archive: an OCI layout tar holding one image.
func Archive(t testing.TB) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, body []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(body)
	}
	digestOf := func(body []byte) string {
		sum := sha256.Sum256(body)
		return hex.EncodeToString(sum[:])
	}
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` +
		digestOf(config) + `","size":` + strconv.Itoa(len(config)) + `},"layers":[]}`)
	add("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	add("index.json", []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:`+
		digestOf(manifest)+`","size":`+strconv.Itoa(len(manifest))+`}]}`))
	add("blobs/sha256/"+digestOf(config), config)
	add("blobs/sha256/"+digestOf(manifest), manifest)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
