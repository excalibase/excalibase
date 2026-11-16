package apphost

import "testing"

func TestLooksLikeCredential(t *testing.T) {
	for name, want := range map[string]bool{
		"API_KEY": true, "db_password": true, "AuthToken": true, "CLIENT_SECRET": true,
		"PASSWD": true, "AWS_CREDENTIAL": true, "MODE": false, "PORT": false,
	} {
		if got := LooksLikeCredential(name); got != want {
			t.Errorf("LooksLikeCredential(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSealCredentialLiterals(t *testing.T) {
	secret, mode := "hunter2", "prod"
	app := &App{ID: "app-1", ProjectID: "proj-1", Env: []EnvVar{
		{Name: "DB_PASSWORD", Kind: KindLiteral, Value: &secret},
		{Name: "MODE", Kind: KindLiteral, Value: &mode},
		{Name: "EMPTY_TOKEN", Kind: KindLiteral},
		{Name: "REF_KEY", Kind: KindReference},
	}}

	sealed := app.SealCredentialLiterals()

	if len(sealed) != 1 {
		t.Fatalf("sealed %d values, want 1: %v", len(sealed), sealed)
	}
	ref := AppSecretRef("proj-1", "app-1", "DB_PASSWORD")
	if sealed[ref] != secret {
		t.Fatalf("the value for %v was not returned: %v", ref, sealed)
	}
	for _, v := range app.Env {
		switch v.Name {
		case "DB_PASSWORD":
			if v.Kind != KindSecret || v.Value != nil {
				t.Errorf("DB_PASSWORD must be a secret without a value, got %+v", v)
			}
		case "MODE":
			if v.Kind != KindLiteral {
				t.Errorf("MODE must stay plain, got %+v", v)
			}
		}
	}
}

func TestSealCredentialLiteralsWithNothingToSeal(t *testing.T) {
	mode := "prod"
	app := &App{Env: []EnvVar{{Name: "MODE", Kind: KindLiteral, Value: &mode}}}
	if sealed := app.SealCredentialLiterals(); len(sealed) != 0 {
		t.Fatalf("sealed %v, want nothing", sealed)
	}
}
