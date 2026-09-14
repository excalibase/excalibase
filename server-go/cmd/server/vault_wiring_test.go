package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

func TestLocalVaultNeedsAutoReady(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.AppConfig
		want bool
	}{
		{"selfhosted on k8s relies on the bootstrap Job", config.AppConfig{DeploymentMode: "selfhosted", ProvisionerMode: "k8s"}, false},
		{"selfhosted on docker auto-readies at boot", config.AppConfig{DeploymentMode: "selfhosted", ProvisionerMode: "docker"}, true},
		{"cloud on k8s relies on the bootstrap Job", config.AppConfig{DeploymentMode: "cloud", ProvisionerMode: "k8s"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := localVaultNeedsAutoReady(tt.cfg); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewLocalVault_AutoReadyInitsAndUnseals(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "unseal.key")

	v, err := newLocalVault(vault.NewMemoryStore(), true, keyPath, "")
	if err != nil {
		t.Fatalf("newLocalVault: %v", err)
	}
	if !v.Initialized() || v.Sealed() {
		t.Fatalf("want initialized+unsealed, got initialized=%v sealed=%v", v.Initialized(), v.Sealed())
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("unseal key must be persisted for restarts: %v", err)
	}
}

func TestNewLocalVault_WithoutAutoReadyLeavesVaultUntouched(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "unseal.key")

	v, err := newLocalVault(vault.NewMemoryStore(), false, keyPath, "")
	if err != nil {
		t.Fatalf("newLocalVault: %v", err)
	}
	if v.Initialized() {
		t.Fatal("cloud must leave init to the bootstrap Job")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("no key file may be written without auto-ready, stat err = %v", err)
	}
}

func TestNewLocalVault_AutoReadyRestartUnsealsFromPersistedKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "unseal.key")
	store := vault.NewMemoryStore()

	first, err := newLocalVault(store, true, keyPath, "")
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	first.Close()

	second, err := newLocalVault(store, true, keyPath, "")
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if second.Sealed() {
		t.Fatal("restart must auto-unseal from the persisted key")
	}
}
