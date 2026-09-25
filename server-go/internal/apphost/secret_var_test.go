package apphost_test

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func TestAppSecretPathSitsUnderTheProjectPrefix(t *testing.T) {
	app := validApp()
	ref := app.SetSecretVar("API_KEY")
	if ref.Path != "projects/proj_abc123/apps/app_01/env/API_KEY" || ref.Key != "value" {
		t.Fatalf("ref = %+v", ref)
	}
	if err := app.Validate(); err != nil {
		t.Fatalf("an app pointing at its own secret entry must validate: %v", err)
	}
}

func TestAppSecretRefSitsUnderTheAppPrefix(t *testing.T) {
	ref := apphost.AppSecretRef("p1", "a1", "TOKEN")
	if prefix := apphost.AppSecretPrefix("p1", "a1"); ref.Path[:len(prefix)] != prefix {
		t.Fatalf("%q is not under %q", ref.Path, prefix)
	}
}

func TestSetSecretVarReplacesAVariableInPlace(t *testing.T) {
	app := validApp()
	app.SetSecretVar("MODE")
	if len(app.Env) != 1 {
		t.Fatalf("env = %+v, want the one variable replaced", app.Env)
	}
	got := app.Env[0]
	if got.Kind != apphost.KindSecret || got.Value != nil || got.Reference != nil || got.Secret == nil {
		t.Fatalf("MODE = %+v, want a secret carrying only its pointer", got)
	}
}

func TestSetSecretVarAppendsANewVariable(t *testing.T) {
	app := validApp()
	app.SetSecretVar("API_KEY")
	if len(app.Env) != 2 || app.Env[1].Name != "API_KEY" || app.Env[1].Kind != apphost.KindSecret {
		t.Fatalf("env = %+v, want API_KEY appended as a secret", app.Env)
	}
}

func TestValidateEnvName(t *testing.T) {
	for _, ok := range []string{"API_KEY", "_x", "a1"} {
		if err := apphost.ValidateEnvName(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "1A", "A-B", "A B", string(make([]byte, apphost.MaxEnvNameLength+1))} {
		if err := apphost.ValidateEnvName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
