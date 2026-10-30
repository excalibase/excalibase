package vault

import (
	"errors"
	"sync"
	"testing"
)

func unsealedVault(t *testing.T) *Vault {
	t.Helper()
	v := tempVault(t)
	result, err := v.Init(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Unseal(result.Shares[0]); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPutIfAbsentWritesOnlyTheFirstTime(t *testing.T) {
	v := unsealedVault(t)
	created, err := v.PutIfAbsent(testVaultKey, map[string]string{"key": "first"})
	if err != nil || !created {
		t.Fatalf("first: created=%v err=%v", created, err)
	}
	created, err = v.PutIfAbsent(testVaultKey, map[string]string{"key": "second"})
	if err != nil || created {
		t.Fatalf("second: created=%v err=%v", created, err)
	}
	if got, _ := v.Get(testVaultKey); got["key"] != "first" {
		t.Fatalf("stored = %v", got)
	}
}

func TestPutIfAbsentLetsExactlyOneConcurrentWriterIn(t *testing.T) {
	v := unsealedVault(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := v.PutIfAbsent(testVaultKey, map[string]string{"key": "x"})
			if err != nil {
				t.Error(err)
			}
			if created {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d writers created the secret", wins)
	}
}

func TestPutIfAbsentNeedsAnUnsealedVault(t *testing.T) {
	v := tempVault(t)
	if _, err := v.PutIfAbsent(testVaultKey, map[string]string{"key": "x"}); !errors.Is(err, ErrSealed) {
		t.Fatalf("err = %v", err)
	}
}
