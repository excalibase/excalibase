package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeVaultAPI answers /api/vault/status and /api/vault/unseal the way the
// running server does, so the CLI is tested against its HTTP contract.
type fakeVaultAPI struct {
	sealed    bool
	gotToken  string
	gotShares []string
}

func (f *fakeVaultAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/vault/status", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"initialized": true, "sealed": f.sealed, "threshold": 1, "progress": 0, "unsealProvider": "manual",
		})
	})
	mux.HandleFunc("/api/vault/unseal", func(w http.ResponseWriter, r *http.Request) {
		f.gotToken = r.Header.Get("Authorization")
		var body struct{ Share string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.gotShares = append(f.gotShares, body.Share)
		if body.Share != "the-key" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid share"})
			return
		}
		f.sealed = false
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"sealed": false, "progress": 1, "threshold": 1})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func secretsFrom(values map[string]string) func(string) (string, error) {
	return func(prompt string) (string, error) {
		for key, value := range values {
			if strings.Contains(strings.ToLower(prompt), key) {
				return value, nil
			}
		}
		return "", errors.New("unexpected prompt " + prompt)
	}
}

func TestVaultCLI_UnsealSendsTheKeyWithTheAdminToken(t *testing.T) {
	api := &fakeVaultAPI{sealed: true}
	server := api.server(t)
	var out bytes.Buffer
	err := runVaultCLI([]string{"unseal", "--url", server.URL}, func(string) string { return "" }, &out,
		secretsFrom(map[string]string{"token": "excali_admin", "unseal key": "the-key"}))
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if api.gotToken != "Bearer excali_admin" {
		t.Errorf("Authorization = %q", api.gotToken)
	}
	if len(api.gotShares) != 1 || api.gotShares[0] != "the-key" {
		t.Errorf("shares sent = %v", api.gotShares)
	}
	if strings.Contains(out.String(), "the-key") || strings.Contains(out.String(), "excali_admin") {
		t.Errorf("output echoes a secret: %s", out.String())
	}
	if !strings.Contains(out.String(), "unsealed") {
		t.Errorf("output does not say the vault is unsealed: %s", out.String())
	}
}

func TestVaultCLI_TokenFromTheEnvironment(t *testing.T) {
	api := &fakeVaultAPI{sealed: true}
	server := api.server(t)
	env := map[string]string{"EXCALIBASE_TOKEN": "excali_env"}
	err := runVaultCLI([]string{"unseal", "--url", server.URL}, func(k string) string { return env[k] }, &bytes.Buffer{},
		secretsFrom(map[string]string{"unseal key": "the-key"}))
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if api.gotToken != "Bearer excali_env" {
		t.Errorf("Authorization = %q", api.gotToken)
	}
}

func TestVaultCLI_RefusedKeyIsAnError(t *testing.T) {
	api := &fakeVaultAPI{sealed: true}
	server := api.server(t)
	var out bytes.Buffer
	err := runVaultCLI([]string{"unseal", "--url", server.URL}, func(string) string { return "" }, &out,
		secretsFrom(map[string]string{"token": "excali_admin", "unseal key": "wrong-key"}))
	if err == nil || !strings.Contains(err.Error(), "invalid share") {
		t.Fatalf("err = %v, want the server's refusal", err)
	}
	if strings.Contains(err.Error(), "wrong-key") {
		t.Error("the error echoes the key")
	}
}

func TestVaultCLI_UnsealOnAnOpenVaultDoesNothing(t *testing.T) {
	api := &fakeVaultAPI{sealed: false}
	server := api.server(t)
	var out bytes.Buffer
	if err := runVaultCLI([]string{"unseal", "--url", server.URL}, func(string) string { return "" }, &out,
		secretsFrom(nil)); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if len(api.gotShares) != 0 || !strings.Contains(out.String(), "already unsealed") {
		t.Errorf("shares=%v out=%s", api.gotShares, out.String())
	}
}

func TestVaultCLI_Status(t *testing.T) {
	api := &fakeVaultAPI{sealed: true}
	server := api.server(t)
	var out bytes.Buffer
	if err := runVaultCLI([]string{"status", "--url", server.URL}, func(string) string { return "" }, &out, secretsFrom(nil)); err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"sealed: true", "unseal provider: manual"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q: %s", want, out.String())
		}
	}
}

func TestVaultCLI_DefaultURLIsTheLocalServer(t *testing.T) {
	if got := vaultCLIDefaultURL(func(k string) string { return map[string]string{"PORT": "24100"}[k] }); got != "http://127.0.0.1:24100" {
		t.Errorf("default URL = %s", got)
	}
	if got := vaultCLIDefaultURL(func(string) string { return "" }); got != "http://127.0.0.1:24005" {
		t.Errorf("default URL = %s", got)
	}
}
