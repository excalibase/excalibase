package main

import "testing"

func TestVaultDBURL(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    string
		wantErr bool
	}{
		{"set", "postgres://vault:pw@db:5432/platform", "postgres://vault:pw@db:5432/platform", false},
		{"trimmed", "  postgres://db/platform  ", "postgres://db/platform", false},
		{"missing is fatal — no file-backed fallback", "", "", true},
		{"blank is fatal", "   ", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("VAULT_DB_URL", tt.env)
			got, err := vaultDBURL()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
