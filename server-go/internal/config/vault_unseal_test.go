package config

import (
	"strings"
	"testing"
)

func lookup(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestParseVaultUnseal(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantErr string
		wantKMS bool
	}{
		{name: "unset keeps the plaintext default", env: map[string]string{}},
		{name: "plaintext", env: map[string]string{"VAULT_UNSEAL_PROVIDER": "plaintext", "VAULT_UNSEAL_KEY": "abc"}},
		{name: "awskms with a key", env: map[string]string{"VAULT_UNSEAL_PROVIDER": "awskms", "VAULT_KMS_KEY_ID": "arn:aws:kms:eu-west-1:1:key/k", "VAULT_UNSEAL_KEY_CIPHERTEXT": "Y3Q="}, wantKMS: true},
		{name: "awskms before the first init has no ciphertext yet", env: map[string]string{"VAULT_UNSEAL_PROVIDER": "awskms", "VAULT_KMS_KEY_ID": "k"}, wantKMS: true},
		{name: "awskms needs a key id", env: map[string]string{"VAULT_UNSEAL_PROVIDER": "awskms"}, wantErr: "VAULT_KMS_KEY_ID"},
		{name: "awskms never falls back to a plaintext key", env: map[string]string{"VAULT_UNSEAL_PROVIDER": "awskms", "VAULT_KMS_KEY_ID": "k", "VAULT_UNSEAL_KEY": "abc"}, wantErr: "VAULT_UNSEAL_KEY"},
		{name: "plaintext refuses a ciphertext", env: map[string]string{"VAULT_UNSEAL_PROVIDER": "plaintext", "VAULT_UNSEAL_KEY_CIPHERTEXT": "Y3Q="}, wantErr: "VAULT_UNSEAL_KEY_CIPHERTEXT"},
		{name: "plaintext refuses a kms key", env: map[string]string{"VAULT_UNSEAL_PROVIDER": "plaintext", "VAULT_KMS_KEY_ID": "k"}, wantErr: "VAULT_KMS_KEY_ID"},
		{name: "a ciphertext without a provider is not guessed", env: map[string]string{"VAULT_UNSEAL_KEY_CIPHERTEXT": "Y3Q="}, wantErr: "VAULT_UNSEAL_PROVIDER"},
		{name: "unknown provider", env: map[string]string{"VAULT_UNSEAL_PROVIDER": "ovhcloud"}, wantErr: "ovhcloud"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseVaultUnseal(lookup(tc.env))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.UsesKMS() != tc.wantKMS {
				t.Fatalf("UsesKMS = %v, want %v", got.UsesKMS(), tc.wantKMS)
			}
		})
	}
}

func TestVaultUnsealCheckDeployment(t *testing.T) {
	kms := VaultUnseal{Provider: UnsealProviderAWSKMS, KMSKeyID: "k"}
	if err := kms.CheckDeployment("k8s", ""); err != nil {
		t.Fatalf("awskms on k8s with the in-process vault: %v", err)
	}
	if err := kms.CheckDeployment("k8s", "http://vault:24010"); err == nil {
		t.Fatal("awskms configures the in-process vault; with a remote vault it would silently do nothing")
	}
	if err := kms.CheckDeployment("docker", ""); err == nil {
		t.Fatal("the docker provisioner keeps its key in a file; awskms there must be refused, not ignored")
	}
	if err := (VaultUnseal{}).CheckDeployment("docker", ""); err != nil {
		t.Fatalf("plaintext default in docker: %v", err)
	}
}

// Manual unseal (EXC-579): the key lives with the admin, never on the server,
// so no setting may hand the process a key or a way to fetch one.
func TestParseVaultUnseal_Manual(t *testing.T) {
	got, err := ParseVaultUnseal(lookup(map[string]string{"VAULT_UNSEAL_PROVIDER": "manual"}))
	if err != nil {
		t.Fatalf("manual: %v", err)
	}
	if !got.Manual() || got.UsesKMS() {
		t.Fatalf("manual parsed as %+v", got)
	}
	for name, env := range map[string]map[string]string{
		"a plaintext key":  {"VAULT_UNSEAL_PROVIDER": "manual", "VAULT_UNSEAL_KEY": "abc"},
		"a KMS ciphertext": {"VAULT_UNSEAL_PROVIDER": "manual", "VAULT_UNSEAL_KEY_CIPHERTEXT": "Y3Q="},
		"a KMS key":        {"VAULT_UNSEAL_PROVIDER": "manual", "VAULT_KMS_KEY_ID": "k"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseVaultUnseal(lookup(env)); err == nil {
				t.Fatalf("manual accepted %s", name)
			}
		})
	}
}

func TestVaultUnsealCheckDeployment_Manual(t *testing.T) {
	manual := VaultUnseal{Provider: UnsealProviderManual}
	for _, mode := range []string{"k8s", "docker"} {
		if err := manual.CheckDeployment(mode, ""); err != nil {
			t.Fatalf("manual on %s: %v", mode, err)
		}
	}
	if err := manual.CheckDeployment("k8s", "http://vault:24010"); err == nil {
		t.Fatal("manual applies to the in-process vault; a remote vault unseals itself")
	}
}
