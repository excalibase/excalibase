//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/natsauth"
)

// natsCredHashOf reads a stored hash straight from the table; only the tests need it.
func natsCredHashOf(ctx context.Context, store *Store, principal string) (string, bool, error) {
	var hash string
	err := store.db.QueryRowContext(ctx,
		`SELECT password_hash FROM nats_credentials WHERE principal = $1`, principal).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return hash, err == nil, err
}

const (
	natsCredProject = "nats-proj-1"
	natsCredHash    = "$2a$10$abcdefghijklmnopqrstuv"
)

func TestNatsCredentials_UpsertRotates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	principal := natsauth.TenantWatcherPrincipal(natsCredProject)

	if err := store.UpsertNatsCredential(ctx, principal, natsCredProject, natsCredHash); err != nil {
		t.Fatalf("UpsertNatsCredential: %v", err)
	}
	hash, found, err := natsCredHashOf(ctx, store, principal)
	if err != nil || !found || hash != natsCredHash {
		t.Fatalf("lookup after insert: hash=%q found=%v err=%v", hash, found, err)
	}

	rotated := natsCredHash + "ROTATED"
	if err := store.UpsertNatsCredential(ctx, principal, natsCredProject, rotated); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	hash, _, _ = natsCredHashOf(ctx, store, principal)
	if hash != rotated {
		t.Errorf("hash after rotation = %q, want the new hash", hash)
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

	if _, found, _ := natsCredHashOf(ctx, store, tenant); found {
		t.Error("tenant credential survived deprovision")
	}
	if _, found, _ := natsCredHashOf(ctx, store, natsauth.PrincipalGraphQL); !found {
		t.Error("service credential was removed with the project")
	}
}

func TestNatsCredentials_DeleteSinglePrincipal(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.UpsertNatsCredential(ctx, natsauth.PrincipalGraphQL, "", natsCredHash); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.DeleteNatsCredential(ctx, natsauth.PrincipalGraphQL); err != nil {
		t.Fatalf("DeleteNatsCredential: %v", err)
	}
	if _, found, _ := natsCredHashOf(ctx, store, natsauth.PrincipalGraphQL); found {
		t.Error("credential survived delete")
	}
}
