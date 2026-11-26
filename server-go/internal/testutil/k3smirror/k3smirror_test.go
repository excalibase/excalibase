package k3smirror

import (
	"strings"
	"testing"
)

func TestRegistriesYAML_SendsDockerHubPullsToTheMirror(t *testing.T) {
	got := registriesYAML("ghcr.io/excalibase/mirror/", "", "")
	for _, want := range []string{
		"docker.io:",
		"- https://ghcr.io",
		`"^(?:library/)?(.*)$": "excalibase/mirror/$1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("registries.yaml lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "auth:") {
		t.Fatalf("no login was given, yet the file carries one:\n%s", got)
	}
}

func TestRegistriesYAML_LogsIntoTheMirrorWhenGivenATokenOnly(t *testing.T) {
	user, token := "ci-user", "tok-"+t.Name()
	got := registriesYAML("ghcr.io/excalibase/mirror", user, token)
	for _, want := range []string{"ghcr.io:", "username: " + user, "password: " + token} {
		if !strings.Contains(got, want) {
			t.Fatalf("registries.yaml lacks %q:\n%s", want, got)
		}
	}
	if half := registriesYAML("ghcr.io/excalibase/mirror", user, ""); strings.Contains(half, "auth:") {
		t.Fatalf("a user without a token must not write a login:\n%s", half)
	}
}

func TestRegistriesYAML_IsEmptyWithoutAMirror(t *testing.T) {
	if got := registriesYAML("", "u", "t"); got != "" {
		t.Fatalf("no prefix, no file; got:\n%s", got)
	}
}

func TestRegistriesYAML_RefusesAPrefixThatIsNotARegistryPath(t *testing.T) {
	if got := registriesYAML("mirror", "", ""); got != "" {
		t.Fatalf("a prefix without a registry host cannot be an endpoint; got:\n%s", got)
	}
}
