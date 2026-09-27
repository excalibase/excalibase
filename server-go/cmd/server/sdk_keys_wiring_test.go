package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

type privateSigningVault struct {
	vaultclient.VaultClient
	pem string
}

func (v privateSigningVault) Get(path string) (map[string]string, error) {
	if path != "pki/signing/private" {
		return nil, errors.New("secret not found")
	}
	return map[string]string{"key": v.pem}, nil
}

func newPrivateSigningVault(t *testing.T) privateSigningVault {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal signing key: %v", err)
	}
	return privateSigningVault{pem: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))}
}

func TestSDKKeyManagerIsUnavailableWithoutAuthURLOrVault(t *testing.T) {
	if buildSDKKeyManager(config.AppConfig{}, bootVault{}) != nil {
		t.Error("built a manager without an auth URL")
	}
	if buildSDKKeyManager(config.AppConfig{AuthInternalURL: "http://auth.invalid"}, nil) != nil {
		t.Error("built a manager without a vault")
	}
}

func TestSDKKeyManagerCallsAuthWithASignedToken(t *testing.T) {
	var path, bearer string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, bearer = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(auth.Close)

	manager := buildSDKKeyManager(config.AppConfig{AuthInternalURL: auth.URL}, newPrivateSigningVault(t))
	if manager == nil {
		t.Fatal("no manager with an auth URL and a vault")
	}
	if _, err := manager.List(context.Background(), "acme", "proj-a"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if path != "/auth/acme/proj-a/api-keys/" || !strings.HasPrefix(bearer, "Bearer ey") {
		t.Fatalf("auth saw %q with %q", path, bearer)
	}
}
