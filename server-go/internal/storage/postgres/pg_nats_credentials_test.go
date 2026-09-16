//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/natsauth"
)

const (
	natsCredProject = "nats-proj-1"
	natsCredHash    = "$2a$10$abcdefghijklmnopqrstuv"
)

func TestNatsCredentials_UpsertRotatesAndLookupFinds(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	principal := natsauth.TenantWatcherPrincipal(natsCredProject)

	if err := store.UpsertNatsCredential(ctx, principal, natsCredProject, natsCredHash); err != nil {
		t.Fatalf("UpsertNatsCredential: %v", err)
	}
	hash, found, err := store.LookupNatsCredentialHash(ctx, principal)
	if err != nil || !found || hash != natsCredHash {
		t.Fatalf("lookup after insert: hash=%q found=%v err=%v", hash, found, err)
	}

	rotated := natsCredHash + "ROTATED"
	if err := store.UpsertNatsCredential(ctx, principal, natsCredProject, rotated); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	hash, _, _ = store.LookupNatsCredentialHash(ctx, principal)
	if hash != rotated {
		t.Errorf("hash after rotation = %q, want the new hash", hash)
	}
}

func TestNatsCredentials_LookupMissingIsNotAnError(t *testing.T) {
	store := testStore(t)
	hash, found, err := store.LookupNatsCredentialHash(context.Background(), "svc-nope")
	if err != nil {
		t.Fatalf("LookupNatsCredentialHash: %v", err)
	}
	if found || hash != "" {
		t.Errorf("unknown principal reported found=%v hash=%q", found, hash)
	}
}

func TestNatsCredentials_DeleteForProjectLeavesServicePrincipals(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	tenant := natsauth.TenantWatcherPrincipal(natsCredProject)

	if err := store.UpsertNatsCredential(ctx, tenant, natsCredProject, natsCredHash); err != nil {
		t.Fatalf("upsert tenant: %v", err)
	}
	if err := store.UpsertNatsCredential(ctx, natsauth.PrincipalGraphQL, "", natsCredHash); err != nil {
		t.Fatalf("upsert service: %v", err)
	}

	if err := store.DeleteNatsCredentialsForProject(ctx, natsCredProject); err != nil {
		t.Fatalf("DeleteNatsCredentialsForProject: %v", err)
	}

	if _, found, _ := store.LookupNatsCredentialHash(ctx, tenant); found {
		t.Error("tenant credential survived deprovision")
	}
	if _, found, _ := store.LookupNatsCredentialHash(ctx, natsauth.PrincipalGraphQL); !found {
		t.Error("service credential was removed with the project")
	}
}

func TestNatsCredentials_DeleteSinglePrincipal(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.UpsertNatsCredential(ctx, natsauth.PrincipalPgDog, "", natsCredHash); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.DeleteNatsCredential(ctx, natsauth.PrincipalPgDog); err != nil {
		t.Fatalf("DeleteNatsCredential: %v", err)
	}
	if _, found, _ := store.LookupNatsCredentialHash(ctx, natsauth.PrincipalPgDog); found {
		t.Error("credential survived delete")
	}
}
