package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/natsauth"
)

type fakeNatsCredStore struct {
	hashes     map[string]string
	projects   map[string]string
	upsertErr  error
	deletedFor []string
}

func newFakeNatsCredStore() *fakeNatsCredStore {
	return &fakeNatsCredStore{hashes: map[string]string{}, projects: map[string]string{}}
}

func (f *fakeNatsCredStore) UpsertNatsCredential(_ context.Context, principal, projectID, hash string) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.hashes[principal] = hash
	f.projects[principal] = projectID
	return nil
}

func (f *fakeNatsCredStore) LookupNatsCredentialHash(_ context.Context, principal string) (string, bool, error) {
	hash, ok := f.hashes[principal]
	return hash, ok, nil
}

func (f *fakeNatsCredStore) DeleteNatsCredential(_ context.Context, principal string) error {
	delete(f.hashes, principal)
	return nil
}

func (f *fakeNatsCredStore) DeleteNatsCredentialsForProject(_ context.Context, projectID string) error {
	f.deletedFor = append(f.deletedFor, projectID)
	return nil
}

func TestMintTenantWatcherStoresOnlyTheHash(t *testing.T) {
	store := newFakeNatsCredStore()
	minter := NewNatsCredentialMinter(store)

	principal, password, err := minter.MintTenantWatcher(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("MintTenantWatcher: %v", err)
	}
	if principal != natsauth.TenantWatcherPrincipal("proj-a") {
		t.Errorf("principal = %q", principal)
	}
	if password == "" {
		t.Fatal("no password returned")
	}
	if store.hashes[principal] == password {
		t.Error("plaintext password was persisted")
	}
	if store.projects[principal] != "proj-a" {
		t.Errorf("project_id = %q, want proj-a", store.projects[principal])
	}
	if err := natsauth.Authenticate(context.Background(), store, principal, password); err != nil {
		t.Errorf("minted credential does not authenticate: %v", err)
	}
}

func TestMintTenantWatcherRotates(t *testing.T) {
	store := newFakeNatsCredStore()
	minter := NewNatsCredentialMinter(store)
	ctx := context.Background()

	_, first, _ := minter.MintTenantWatcher(ctx, "proj-a")
	principal, second, _ := minter.MintTenantWatcher(ctx, "proj-a")

	if first == second {
		t.Error("re-provision reused the old password")
	}
	if natsauth.Authenticate(ctx, store, principal, first) == nil {
		t.Error("old password still authenticates after rotation")
	}
	if err := natsauth.Authenticate(ctx, store, principal, second); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
}

func TestMintTenantWatcherRejectsUnsafeProjectID(t *testing.T) {
	minter := NewNatsCredentialMinter(newFakeNatsCredStore())
	for _, projectID := range []string{"", "a.b", "a b", "*", ">", "-lead"} {
		if _, _, err := minter.MintTenantWatcher(context.Background(), projectID); err == nil {
			t.Errorf("project id %q accepted", projectID)
		}
	}
}

func TestMintTenantWatcherPropagatesStoreFailure(t *testing.T) {
	store := newFakeNatsCredStore()
	store.upsertErr = errors.New("db down")
	minter := NewNatsCredentialMinter(store)

	if _, _, err := minter.MintTenantWatcher(context.Background(), "proj-a"); err == nil {
		t.Error("store failure was swallowed")
	}
}

func TestRevokeProjectDeletesCredentials(t *testing.T) {
	store := newFakeNatsCredStore()
	minter := NewNatsCredentialMinter(store)

	if err := minter.RevokeProject(context.Background(), "proj-a"); err != nil {
		t.Fatalf("RevokeProject: %v", err)
	}
	if len(store.deletedFor) != 1 || store.deletedFor[0] != "proj-a" {
		t.Errorf("deleted for %v, want [proj-a]", store.deletedFor)
	}
}

func TestNilMinterIsANoOp(t *testing.T) {
	var minter *NatsCredentialMinter
	if NewNatsCredentialMinter(nil) != nil {
		t.Error("NewNatsCredentialMinter(nil) should yield a nil minter")
	}
	principal, password, err := minter.MintTenantWatcher(context.Background(), "proj-a")
	if err != nil || principal != "" || password != "" {
		t.Errorf("nil minter returned (%q, %q, %v)", principal, password, err)
	}
	if err := minter.RevokeProject(context.Background(), "proj-a"); err != nil {
		t.Errorf("nil minter revoke: %v", err)
	}
}
