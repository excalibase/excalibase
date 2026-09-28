package vault

import (
	"errors"
	"strings"
	"testing"
)

// reverseWrap stands in for a KMS: it hides the share behind a reversible
// transform so the test can unwrap it again.
func reverseWrap(shares []string) ([]string, error) {
	out := make([]string, len(shares))
	for i, share := range shares {
		out[i] = "wrapped:" + share
	}
	return out, nil
}

func TestInitWrappedReturnsOnlyWrappedShares(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	result, err := v.InitWrapped(1, 1, reverseWrap)
	if err != nil {
		t.Fatalf("InitWrapped: %v", err)
	}
	if len(result.Shares) != 1 || !strings.HasPrefix(result.Shares[0], "wrapped:") {
		t.Fatalf("shares = %v, want only wrapped values", result.Shares)
	}
	if v.Sealed() {
		t.Fatal("vault must be unsealed after init")
	}

	v.Seal()
	share := strings.TrimPrefix(result.Shares[0], "wrapped:")
	if progress, err := v.Unseal(share); err != nil || !progress.Done {
		t.Fatalf("unwrapped share must unseal: progress=%v err=%v", progress, err)
	}
}

func TestInitWrappedLeavesTheVaultUninitializedWhenWrappingFails(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	failing := func([]string) ([]string, error) { return nil, errors.New("kms unavailable") }
	if _, err := v.InitWrapped(1, 1, failing); err == nil {
		t.Fatal("InitWrapped must fail when the shares cannot be wrapped")
	}
	if v.Initialized() {
		t.Fatal("a vault whose only share could not be wrapped must not be initialized: nobody could ever unseal it")
	}
}

func TestInitWrappedRefusesAWrapperThatDropsShares(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	short := func([]string) ([]string, error) { return []string{}, nil }
	if _, err := v.InitWrapped(1, 1, short); err == nil {
		t.Fatal("InitWrapped must refuse a wrapper that returns fewer shares")
	}
	if v.Initialized() {
		t.Fatal("vault must stay uninitialized")
	}
}

// failingStore answers every barrier read with an error, as a platform DB
// that is down at boot does.
type failingStore struct{ VaultStore }

func (failingStore) GetBarrier() ([]byte, []byte, error) {
	return nil, nil, errors.New("connection refused")
}

func TestCheckInitializedReportsAStoreFailure(t *testing.T) {
	v := &Vault{store: failingStore{NewMemoryStore()}}
	if _, err := v.CheckInitialized(); err == nil {
		t.Fatal("a store error must not read as 'not initialized'")
	}
	ok := tempVault(t)
	if initialized, err := ok.CheckInitialized(); err != nil || initialized {
		t.Fatalf("fresh vault: initialized=%v err=%v", initialized, err)
	}
}
