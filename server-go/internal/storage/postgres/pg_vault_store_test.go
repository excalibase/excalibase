//go:build integration

package postgres

import (
	"testing"

	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// The vault data must outlive a Vault handle: a restarted server opens a new
// handle on the same platform DB and must find the barrier and secrets there.
func TestVaultPostgresStore_PersistsAcrossHandles(t *testing.T) {
	store := testStore(t)

	first, err := vault.NewWithStore(vault.NewPostgresStore(store.DB()))
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	result, err := first.Init(1, 1)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := first.Put("projects/app/credentials/admin", map[string]string{"password": "hunter2"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	first.Close()

	second, err := vault.NewWithStore(vault.NewPostgresStore(store.DB()))
	if err != nil {
		t.Fatalf("reopen vault: %v", err)
	}
	if !second.Initialized() || !second.Sealed() {
		t.Fatalf("reopened handle want initialized+sealed, got initialized=%v sealed=%v", second.Initialized(), second.Sealed())
	}
	if _, err := second.Unseal(result.Shares[0]); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	data, err := second.Get("projects/app/credentials/admin")
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if data["password"] != "hunter2" {
		t.Fatalf("password = %q, want hunter2", data["password"])
	}
}

func TestVaultPostgresStore_PrefixSemantics(t *testing.T) {
	pg := vault.NewPostgresStore(testStore(t).DB())
	for _, path := range []string{"projects/a/x", "projects/a/y", "projects/b/x"} {
		if err := pg.PutSecret(path, []byte("v")); err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
	}

	if _, err := pg.DeletePrefix(""); err == nil {
		t.Fatal("empty prefix must be rejected")
	}
	deleted, err := pg.DeletePrefix("projects/a/")
	if err != nil || deleted != 2 {
		t.Fatalf("DeletePrefix = (%d, %v), want (2, nil)", deleted, err)
	}
	remaining, err := pg.ListSecrets("")
	if err != nil || len(remaining) != 1 || remaining[0] != "projects/b/x" {
		t.Fatalf("remaining = %v, %v", remaining, err)
	}
}
