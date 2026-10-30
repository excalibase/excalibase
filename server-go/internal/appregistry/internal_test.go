package appregistry

import (
	"errors"
	"strings"
	"testing"
)

func TestInternal_NamesItsHostInEverySpelling(t *testing.T) {
	internal, err := NewInternal("Registry.Excalibase.Internal", []string{"zot.excalibase.svc.cluster.local", "10.43.200.10"})
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
	for _, host := range []string{"", "docker.io", "index.docker.io", "bad host", "ghcr.io/x"} {
		if _, err := NewInternal(host, nil); err == nil {
			t.Errorf("%q accepted as the internal registry", host)
		}
	}
	guard, err := NewInternal("registry.excalibase.internal", nil)
	if err != nil || guard.CanIssue() || !guard.Names("registry.excalibase.internal/x:1") {
		t.Errorf("a guard with no issuer still names its host: %v", err)
	}
	if _, err := guard.PullCredential("proj-a"); err == nil {
		t.Error("a guard issues no pull credential")
	}
	if _, err := guard.VerifyPull("proj-a", "x"); err == nil {
		t.Error("a guard verifies no pull credential")
	}
}

func TestInternal_ImageAndPullCredential(t *testing.T) {
	internal, issuer := testInternal(t, fixedGenerations{"proj-b": 3})
	image, err := internal.Image("proj-a", "api", "sha256:"+hex64)
	if err != nil || image != "registry.excalibase.internal/proj-a/api@sha256:"+hex64 {
		t.Fatalf("image %q: %v", image, err)
	}
	if _, err := internal.Image("proj-a", "api", "latest"); err == nil {
		t.Error("an internal image is always a digest")
	}
	cred, err := internal.PullCredential("proj-a")
	if err != nil || !issuer.VerifyPull("proj-a", 0, cred.Password) {
		t.Errorf("the pull secret's password opens the project's token: %v", err)
	}
	rotated, _ := internal.PullCredential("proj-b")
	if ok, _ := internal.VerifyPull("proj-b", rotated.Password); !ok || issuer.VerifyPull("proj-b", 0, rotated.Password) {
		t.Error("a rotated project's credential is its current generation's")
	}
	if _, err := internal.PullCredential("proj-sealed"); err == nil {
		t.Error("an unreadable generation is an error")
	}
}

const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestInternal_NilIsNoRegistry(t *testing.T) {
	var none *Internal
	if none.NamesHost("registry.excalibase.internal") || none.Names("registry.excalibase.internal/p/a@sha256:x") || none.CanIssue() {
		t.Error("no registry names nothing and issues nothing")
	}
	if _, err := none.Image("proj-a", "api", "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Error("no registry has no images")
	}
}
