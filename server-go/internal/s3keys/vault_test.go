package s3keys

import (
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/pkg/vault"
)

func unsealedVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	result, err := v.Init(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Unseal(result.Shares[0]); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVaultSecrets_RoundTripUnderTheProjectPrefix(t *testing.T) {
	v := unsealedVault(t)
	secrets := NewVaultSecrets(v)
	if err := secrets.Put("proj-1", "EXS3AAAAAAAAAAAAAAAA", "s3cr3t"); err != nil {
		t.Fatal(err)
	}
	if got, err := secrets.Get("proj-1", "EXS3AAAAAAAAAAAAAAAA"); err != nil || got != "s3cr3t" {
		t.Fatalf("got %q %v", got, err)
	}
	listed, err := v.List("projects/proj-1/")
	if err != nil || len(listed) != 1 {
		t.Fatalf("the secret sits under the project's prefix, which its teardown removes: %v %v", listed, err)
	}
	if err := secrets.Delete("proj-1", "EXS3AAAAAAAAAAAAAAAA"); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get("proj-1", "EXS3AAAAAAAAAAAAAAAA"); !errors.Is(err, ErrSecretMissing) {
		t.Fatalf("err = %v", err)
	}
	if err := secrets.Delete("proj-1", "EXS3AAAAAAAAAAAAAAAA"); err != nil {
		t.Fatalf("deleting twice is fine: %v", err)
	}
}

func TestVaultSecrets_SealedIsAFailureNotAMissingSecret(t *testing.T) {
	v := unsealedVault(t)
	secrets := NewVaultSecrets(v)
	_ = secrets.Put("proj-1", "EXS3AAAAAAAAAAAAAAAA", "s3cr3t")
	v.Seal()
	_, err := secrets.Get("proj-1", "EXS3AAAAAAAAAAAAAAAA")
	if err == nil || errors.Is(err, ErrSecretMissing) {
		t.Fatalf("err = %v", err)
	}
}
