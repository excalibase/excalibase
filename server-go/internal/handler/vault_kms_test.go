package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// prefixEncrypter stands in for KMS: the "ciphertext" is the share behind a
// marker, which lets the test prove the plaintext share never left the handler.
type prefixEncrypter struct{ err error }

func (e prefixEncrypter) Encrypt(_ context.Context, keyID string, plaintext []byte) ([]byte, error) {
	if e.err != nil {
		return nil, e.err
	}
	return []byte("kms(" + keyID + "):" + strings.Repeat("*", len(plaintext))), nil
}

func kmsVaultHandler(t *testing.T, enc prefixEncrypter) (*VaultHandler, *vault.Vault) {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	h := NewVaultHandler(v)
	h.SetUnsealKMS(enc, "arn:aws:kms:eu-west-1:1:key/k")
	return h, v
}

func postVault(handlerFunc http.HandlerFunc, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handlerFunc(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	return w
}

func TestVaultInitUnderKMSReturnsOnlyTheWrappedShare(t *testing.T) {
	h, v := kmsVaultHandler(t, prefixEncrypter{})

	w := postVault(h.Init, `{"shares":1,"threshold":1}`)
	if w.Code != http.StatusOK {
		t.Fatalf("init: %d %s", w.Code, w.Body)
	}
	var body struct {
		Shares        []string `json:"shares"`
		WrappedShares []string `json:"wrappedShares"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Shares) != 0 {
		t.Fatalf("a KMS vault must never return a plaintext share, got %v", body.Shares)
	}
	if len(body.WrappedShares) != 1 || body.WrappedShares[0] == "" {
		t.Fatalf("wrappedShares = %v, want one ciphertext", body.WrappedShares)
	}
	if !v.Initialized() || v.Sealed() {
		t.Fatal("vault must be initialized and unsealed after init")
	}
}

func TestVaultInitUnderKMSRefusesMoreThanOneShare(t *testing.T) {
	h, v := kmsVaultHandler(t, prefixEncrypter{})

	if w := postVault(h.Init, `{"shares":5,"threshold":3}`); w.Code != http.StatusBadRequest {
		t.Fatalf("init 5/3 under KMS: %d, want 400 — boot unseals from one ciphertext", w.Code)
	}
	if v.Initialized() {
		t.Fatal("refused init must not initialize the vault")
	}
}

func TestVaultInitUnderKMSStaysUninitializedWhenKMSFails(t *testing.T) {
	h, v := kmsVaultHandler(t, prefixEncrypter{err: errors.New("AccessDeniedException")})

	if w := postVault(h.Init, `{"shares":1,"threshold":1}`); w.Code < 500 {
		t.Fatalf("init with KMS down: %d, want 5xx", w.Code)
	}
	if v.Initialized() {
		t.Fatal("an init whose share could not be wrapped must leave the vault uninitialized")
	}
}

func TestVaultRekeyIsRefusedUnderKMS(t *testing.T) {
	h, _ := kmsVaultHandler(t, prefixEncrypter{})
	if w := postVault(h.Init, `{"shares":1,"threshold":1}`); w.Code != http.StatusOK {
		t.Fatalf("init: %d", w.Code)
	}
	if w := postVault(h.Rekey, `{"shares":1,"threshold":1}`); w.Code != http.StatusConflict {
		t.Fatalf("rekey under KMS: %d, want 409 — it would hand out a plaintext share and orphan the stored ciphertext", w.Code)
	}
}
