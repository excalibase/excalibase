package apphost

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPinImage(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	other := "sha256:" + strings.Repeat("cd", 32)
	for image, want := range map[string]string{
		"ghcr.io/acme/web:main":                  "ghcr.io/acme/web@" + digest,
		"localhost:5000/web:1":                   "localhost:5000/web@" + digest,
		"nginx:1.27":                             "nginx@" + digest,
		"ghcr.io/acme/web@" + other:              "ghcr.io/acme/web@" + digest,
		"registry.example.com:8443/a/b/c:v1.2.3": "registry.example.com:8443/a/b/c@" + digest,
	} {
		got := PinImage(image, digest)
		if got != want {
			t.Errorf("PinImage(%q) = %q, want %q", image, got, want)
		}
		if err := ValidateImageReference(got); err != nil {
			t.Errorf("PinImage(%q) is not a valid reference: %v", image, err)
		}
	}
}

func TestValidateCommitSHA(t *testing.T) {
	for _, ok := range []string{"", "9fceb02", "9fceb02d0ae598e95dc970b74767f19372d61af8", strings.Repeat("a", 64)} {
		if err := ValidateCommitSHA(ok); err != nil {
			t.Errorf("ValidateCommitSHA(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"9fceb0", "main", "9FCEB02D", "9fceb02;rm", strings.Repeat("a", 65), "9fceb02 "} {
		if err := ValidateCommitSHA(bad); err == nil {
			t.Errorf("ValidateCommitSHA(%q) must be refused", bad)
		}
	}
}

func TestDeployOriginIsInTheDeployJSON(t *testing.T) {
	deploy := Deploy{Source: DeploySourceImageWatcher, CommitSHA: "9fceb02", ImageRef: "ghcr.io/a/b:main", Digest: "sha256:" + strings.Repeat("ab", 32)}
	raw, err := json.Marshal(deploy)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"source":"image-watcher"`, `"commitSha":"9fceb02"`, `"imageRef":"ghcr.io/a/b:main"`, `"digest":"sha256:`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("deploy JSON lacks %s: %s", field, raw)
		}
	}
}

func TestIsDeploySource(t *testing.T) {
	for _, source := range []string{DeploySourceStudio, DeploySourceAPI, DeploySourceImageWatcher} {
		if !IsDeploySource(source) {
			t.Errorf("%q is a deploy source", source)
		}
	}
	for _, source := range []string{"", "ci", "Studio"} {
		if IsDeploySource(source) {
			t.Errorf("%q is not a deploy source", source)
		}
	}
}
