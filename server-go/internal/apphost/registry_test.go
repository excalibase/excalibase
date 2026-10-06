package apphost_test

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func TestImageRegistry(t *testing.T) {
	for image, want := range map[string]string{
		"nginx:1.27":                      "docker.io",
		"library/nginx:1.27":              "docker.io",
		"acme/web:1":                      "docker.io",
		"docker.io/acme/web:1":            "docker.io",
		"index.docker.io/acme/web:1":      "docker.io",
		"registry-1.docker.io/acme/web:1": "docker.io",
		"ghcr.io/acme/web:1":              "ghcr.io",
		"GHCR.io/acme/web:1":              "ghcr.io",
		"localhost:5000/web:1":            "localhost:5000",
		"registry.example.com:8443/a/b@sha256:" + strings.Repeat("a", 64): "registry.example.com:8443",
	} {
		if got := apphost.ImageRegistry(image); got != want {
			t.Errorf("ImageRegistry(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestNormalizeRegistry(t *testing.T) {
	for raw, want := range map[string]string{
		"ghcr.io":              "ghcr.io",
		"GHCR.IO":              "ghcr.io",
		"docker.io":            "docker.io",
		"index.docker.io":      "docker.io",
		"registry-1.docker.io": "docker.io",
		"localhost:5000":       "localhost:5000",
	} {
		got, err := apphost.NormalizeRegistry(raw)
		if err != nil || got != want {
			t.Errorf("NormalizeRegistry(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "https://ghcr.io", "ghcr.io/acme", "ghcr.io:", "-ghcr.io", "ghcr io", "..", "a/../b",
		strings.Repeat("a", 256) + ".io", "ghcr.io:999999",
	} {
		if _, err := apphost.NormalizeRegistry(bad); err == nil {
			t.Errorf("NormalizeRegistry(%q) must be refused", bad)
		}
	}
}

func TestRegistryCredentialPath(t *testing.T) {
	if got := apphost.RegistryCredentialPath("p1", "ghcr.io"); got != "projects/p1/registries/ghcr.io" {
		t.Fatalf("path = %q", got)
	}
	if got := apphost.RegistryCredentialPrefix("p1"); got != "projects/p1/registries/" {
		t.Fatalf("prefix = %q", got)
	}
}

func TestRegistryCredentialValidate(t *testing.T) {
	ok := apphost.RegistryCredential{Username: "octocat", Password: "ghp_token"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid credential refused: %v", err)
	}
	for name, bad := range map[string]apphost.RegistryCredential{
		"no username":     {Password: "x"},
		"no password":     {Username: "x"},
		"colon in user":   {Username: "a:b", Password: "x"},
		"control in user": {Username: "a\nb", Password: "x"},
		"long user":       {Username: strings.Repeat("u", apphost.MaxRegistryUsernameLength+1), Password: "x"},
		"long password":   {Username: "u", Password: strings.Repeat("p", apphost.MaxRegistryPasswordLength+1)},
		"control in pass": {Username: "u", Password: "a\x00b"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// A pasted token often carries a trailing newline or space; the registry would
// refuse the login much later, so the space is refused now, by name.
func TestRegistryCredentialRefusesSurroundingWhitespace(t *testing.T) {
	for name, bad := range map[string]apphost.RegistryCredential{
		"user leading space":  {Username: " octocat", Password: "x"},
		"user trailing space": {Username: "octocat ", Password: "x"},
		"pass trailing space": {Username: "octocat", Password: "ghp_token "},
		"pass leading tab":    {Username: "octocat", Password: "\tghp_token"},
		"pass trailing LF":    {Username: "octocat", Password: "ghp_token\n"},
	} {
		err := bad.Validate()
		if err == nil || !strings.Contains(err.Error(), "space") {
			t.Errorf("%s: got %v, want a refusal naming the surrounding space", name, err)
		}
	}
}
