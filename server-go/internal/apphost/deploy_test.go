package apphost

import "testing"

func TestEnvNames_OrderedNamesOnlyNoValues(t *testing.T) {
	secret := "sk_live_should_never_appear"
	env := []EnvVar{
		{Name: "PORT", Kind: KindLiteral, Value: strPtr("8080")},
		{Name: "STRIPE_KEY", Kind: KindLiteral, Value: &secret},
		{Name: "DATABASE_URL", Kind: KindReference, Reference: &ReferenceTarget{
			SourceKind: SourceDatabase, SourceName: "db1", Variable: "DATABASE_URL",
		}},
	}

	names := EnvNames(env)

	want := []string{"PORT", "STRIPE_KEY", "DATABASE_URL"}
	if len(names) != len(want) {
		t.Fatalf("names: got %v want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Errorf("names[%d]: got %q want %q", i, names[i], name)
		}
	}
	for _, name := range names {
		if name == "8080" || name == secret {
			t.Fatalf("EnvNames must never return a value, got %q", name)
		}
	}
}

func TestEnvNames_EmptyForNoEnv(t *testing.T) {
	names := EnvNames(nil)
	if len(names) != 0 {
		t.Fatalf("expected no names, got %v", names)
	}
}

func strPtr(s string) *string { return &s }
