package apphost

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateEnvValueAllowsTextAndLineBreaks(t *testing.T) {
	for _, value := range []string{"", "plain", "a\tb", "line1\nline2\r\n", "-----BEGIN KEY-----\nabc\n-----END KEY-----\n", "ünïcode ✓"} {
		if err := ValidateEnvValue(value); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
}

func TestValidateEnvValueRefusesControlCharacters(t *testing.T) {
	for _, value := range []string{"a\x00b", "\x00", "bell\x07", "esc\x1b[31m", "del\x7f", "c1\u0085"} {
		err := ValidateEnvValue(value)
		if !errors.Is(err, ErrEnvValueControlCharacter) {
			t.Errorf("%q: err = %v, want ErrEnvValueControlCharacter", value, err)
		}
	}
}

// A literal with a NUL is refused when the app is validated, before it can
// reach the store (where Postgres would refuse it as a server error).
func TestValidateEnvRefusesALiteralWithAControlCharacter(t *testing.T) {
	value := "secret\x00tail"
	err := validateEnv("proj_a", "app1", []EnvVar{{Name: "TOKEN", Kind: KindLiteral, Value: &value}})
	if !errors.Is(err, ErrEnvValueControlCharacter) || !strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("err = %v, want ErrEnvValueControlCharacter naming the variable", err)
	}
}
