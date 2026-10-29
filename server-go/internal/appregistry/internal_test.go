package appregistry

import (
	"errors"
	"testing"
)

func TestInternal_NamesItsHostInEverySpelling(t *testing.T) {
	issuer, _ := testIssuer(t)
	internal, err := NewInternal("Registry.Excalibase.Internal", issuer)
	if err != nil {
		t.Fatal(err)
	}
	for image, want := range map[string]bool{
		"registry.excalibase.internal/proj-a/api@sha256:" + hex64: true,
		"REGISTRY.excalibase.internal/proj-a/api:main":            true,
		"ghcr.io/acme/api:1": false,
		"nginx:1.27":         false,
		"registry.excalibase.internal.evil.io/x:1": false,
	} {
		if got := internal.Names(image); got != want {
			t.Errorf("%s: %v, want %v", image, got, want)
		}
	}
	if err := internal.RefuseUserImage("registry.excalibase.internal/proj-b/api:main"); !errors.Is(err, ErrInternalImage) {
		t.Errorf("a user naming the internal registry: %v", err)
	}
	if err := internal.RefuseUserImage("ghcr.io/acme/api:1"); err != nil {
		t.Error(err)
	}
	var none *Internal
	if none.Names("registry.excalibase.internal/x:1") || none.RefuseUserImage("x:1") != nil {
		t.Error("no registry configured names nothing")
	}
}

func TestNewInternal_RefusesAHostThatIsNotOurs(t *testing.T) {
	issuer, _ := testIssuer(t)
	for _, host := range []string{"", "docker.io", "index.docker.io", "bad host", "ghcr.io/x"} {
		if _, err := NewInternal(host, issuer); err == nil {
			t.Errorf("%q accepted as the internal registry", host)
		}
	}
	if _, err := NewInternal("registry.excalibase.internal", nil); err == nil {
		t.Error("no issuer")
	}
}

func TestInternal_ImageAndPullCredential(t *testing.T) {
	issuer, _ := testIssuer(t)
	internal, _ := NewInternal("registry.excalibase.internal", issuer)
	image, err := internal.Image("proj-a", "api", "sha256:"+hex64)
	if err != nil || image != "registry.excalibase.internal/proj-a/api@sha256:"+hex64 {
		t.Fatalf("image %q: %v", image, err)
	}
	if _, err := internal.Image("proj-a", "api", "latest"); err == nil {
		t.Error("an internal image is always a digest")
	}
	cred := internal.PullCredential("proj-a")
	if !issuer.VerifyPull("proj-a", cred.Password) {
		t.Error("the pull secret's password opens the project's token")
	}
}

const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
