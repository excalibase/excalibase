package kmsseal

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// xorCipher is a reversible stand-in for KMS: Encrypt and Decrypt are the
// same byte flip, so a wrapped share can be unwrapped without a network.
type xorCipher struct {
	decryptErr error
	gotKeyID   string
}

func flip(in []byte) []byte {
	out := make([]byte, len(in))
	for i, b := range in {
		out[i] = b ^ 0x5a
	}
	return out
}

func (c *xorCipher) Encrypt(_ context.Context, keyID string, plaintext []byte) ([]byte, error) {
	c.gotKeyID = keyID
	return flip(plaintext), nil
}

func (c *xorCipher) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	if c.decryptErr != nil {
		return nil, c.decryptErr
	}
	return flip(ciphertext), nil
}

func newTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	return v
}

// initWrapped initializes v through the KMS wrapper and returns the single
// base64 ciphertext bootstrap would store, with the vault sealed again as it
// is after a restart.
func initWrapped(t *testing.T, v *vault.Vault, c *xorCipher) string {
	t.Helper()
	res, err := v.InitWrapped(1, 1, ShareWrapper(context.Background(), c, "arn:aws:kms:eu-west-1:1:key/k"))
	if err != nil {
		t.Fatalf("InitWrapped: %v", err)
	}
	v.Seal()
	return res.Shares[0]
}

func TestShareWrapperEncryptsEveryShareUnderTheConfiguredKey(t *testing.T) {
	c := &xorCipher{}
	wrapped, err := ShareWrapper(context.Background(), c, "key-1")([]string{"aa", "bb"})
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if c.gotKeyID != "key-1" {
		t.Fatalf("encrypted under %q, want key-1", c.gotKeyID)
	}
	for i, plain := range []string{"aa", "bb"} {
		if wrapped[i] == plain || strings.Contains(wrapped[i], plain) {
			t.Fatalf("share %d leaked in the clear: %q", i, wrapped[i])
		}
		raw, err := base64.StdEncoding.DecodeString(wrapped[i])
		if err != nil || string(flip(raw)) != plain {
			t.Fatalf("share %d does not round-trip: %q", i, wrapped[i])
		}
	}
}

func TestUnsealAtBootOpensTheVaultFromItsCiphertext(t *testing.T) {
	c := &xorCipher{}
	v := newTestVault(t)
	ct := initWrapped(t, v, c)

	if err := UnsealAtBoot(context.Background(), v, c, ct); err != nil {
		t.Fatalf("UnsealAtBoot: %v", err)
	}
	if v.Sealed() {
		t.Fatal("vault must be unsealed")
	}
}

func TestUnsealAtBootRefusesAnInitializedVaultWithoutCiphertext(t *testing.T) {
	c := &xorCipher{}
	v := newTestVault(t)
	initWrapped(t, v, c)

	err := UnsealAtBoot(context.Background(), v, c, "")
	if !errors.Is(err, ErrCiphertextMissing) {
		t.Fatalf("err = %v, want ErrCiphertextMissing", err)
	}
}

func TestUnsealAtBootRefusesCiphertextKMSCannotDecrypt(t *testing.T) {
	c := &xorCipher{}
	v := newTestVault(t)
	ct := initWrapped(t, v, c)

	c.decryptErr = errors.New("InvalidCiphertextException")
	if err := UnsealAtBoot(context.Background(), v, c, ct); err == nil || !v.Sealed() {
		t.Fatalf("an undecryptable ciphertext must refuse boot: err=%v sealed=%v", err, v.Sealed())
	}
}

func TestUnsealAtBootRefusesCiphertextThatIsNotBase64(t *testing.T) {
	c := &xorCipher{}
	v := newTestVault(t)
	initWrapped(t, v, c)

	if err := UnsealAtBoot(context.Background(), v, c, "not base64!"); err == nil {
		t.Fatal("a corrupted ciphertext must refuse boot")
	}
}

func TestUnsealAtBootRefusesAKeyThatDoesNotOpenThisVault(t *testing.T) {
	c := &xorCipher{}
	other := newTestVault(t)
	foreign := initWrapped(t, other, c)
	v := newTestVault(t)
	initWrapped(t, v, c)

	if err := UnsealAtBoot(context.Background(), v, c, foreign); err == nil || !v.Sealed() {
		t.Fatalf("a ciphertext from another vault must refuse boot: err=%v", err)
	}
}

func TestUnsealAtBootLeavesAnUninitializedVaultToBootstrap(t *testing.T) {
	c := &xorCipher{decryptErr: errors.New("must not be called")}
	v := newTestVault(t)

	if err := UnsealAtBoot(context.Background(), v, c, ""); err != nil {
		t.Fatalf("an uninitialized vault is bootstrap's to initialize: %v", err)
	}
}

// brokenStoreVault reads as unknown: the store could not be asked.
type brokenStoreVault struct{ *vault.Vault }

func (brokenStoreVault) CheckInitialized() (bool, error) { return false, errors.New("db down") }

func TestUnsealAtBootRefusesWhenItCannotTellWhetherTheVaultIsInitialized(t *testing.T) {
	c := &xorCipher{}
	v := newTestVault(t)
	ct := initWrapped(t, v, c)
	if err := UnsealAtBoot(context.Background(), brokenStoreVault{v}, c, ct); err == nil {
		t.Fatal("a platform DB error at boot must refuse start, not skip the unseal")
	}
}
