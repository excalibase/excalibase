package sitebuilds

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

type pathVault struct {
	vaultclient.VaultClient
	values map[string]map[string]string
	err    error
}

func (p pathVault) Get(path string) (map[string]string, error) {
	if p.err != nil {
		return nil, p.err
	}
	values, held := p.values[path]
	if !held {
		return nil, vault.ErrNotFound
	}
	return values, nil
}

// Build-visible secrets are read where the project's edge-function secrets live.
func TestVaultSecrets(t *testing.T) {
	stored := pathVault{values: map[string]map[string]string{
		"projects/p1/edgefn/secrets": {"STRIPE_PK": "pk"},
		"projects/p3/edgefn/secrets": nil,
	}}
	secrets := NewVaultSecrets(stored)
	if got, err := secrets.Secrets(context.Background(), "p1"); err != nil || got["STRIPE_PK"] != "pk" {
		t.Errorf("p1: %v %v", got, err)
	}
	for _, project := range []string{"p2", "p3"} {
		if got, err := secrets.Secrets(context.Background(), project); err != nil || got == nil || len(got) != 0 {
			t.Errorf("%s has none: %v %v", project, got, err)
		}
	}
	if _, err := NewVaultSecrets(pathVault{err: errors.New("sealed")}).Secrets(context.Background(), "p1"); err == nil {
		t.Error("a vault failure must not read as no secrets")
	}
	if _, err := NewVaultSecrets(nil).Secrets(context.Background(), "p1"); err == nil {
		t.Error("no vault must fail")
	}
}
