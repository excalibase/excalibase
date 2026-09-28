package pgscram

import (
	"strings"
	"testing"
)

func fixedSalt() []byte {
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i)
	}
	return salt
}

// A verifier Postgres itself would store for the password, checked against
// an independent derivation (Python hashlib) with a fixed salt.
func TestVerifierMatchesAnIndependentDerivation(t *testing.T) {
	got, err := VerifierWithSalt("correct-horse-battery", fixedSalt())
	if err != nil {
		t.Fatal(err)
	}
	const want = "SCRAM-SHA-256$4096:AAECAwQFBgcICQoLDA0ODw==$eYkIrwlo2T0d6Ca9d6/xzsgUUbZwOdz0/X0v8MioG9w=:TNz7/wgi5sczC08YCovXDhkXvBBmBt7YYvSKSkQAFg8="
	if got != want {
		t.Fatalf("verifier:\n got %s\nwant %s", got, want)
	}
}

// Postgres runs SASLprep on the password before deriving the key, as the
// client does when it logs in; a verifier made from the raw bytes would
// refuse the right password.
func TestVerifierAppliesSASLprepLikePostgres(t *testing.T) {
	for _, pair := range [][2]string{
		{"\uFF41\uFF42\uFF43-pass", "abc-pass"}, // compatibility mapping (NFKC)
		{"soft\u00ADhyphen", "softhyphen"},      // mapped to nothing
		{"non\u00A0breaking", "non breaking"},   // non-ASCII space to space
	} {
		prepared, err := VerifierWithSalt(pair[0], fixedSalt())
		if err != nil {
			t.Fatal(err)
		}
		plain, err := VerifierWithSalt(pair[1], fixedSalt())
		if err != nil {
			t.Fatal(err)
		}
		if prepared != plain {
			t.Errorf("%q was not prepared to %q", pair[0], pair[1])
		}
	}
}

// Postgres uses the raw bytes when SASLprep refuses the password.
func TestVerifierUsesRawBytesWhenSASLprepRefuses(t *testing.T) {
	if _, err := VerifierWithSalt("bell\u0007inside", fixedSalt()); err != nil {
		t.Fatalf("a prohibited character must fall back to the raw password: %v", err)
	}
}

func TestVerifierSaltsEachCall(t *testing.T) {
	first, err := Verifier("same-password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Verifier("same-password")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "SCRAM-SHA-256$4096:") {
		t.Fatalf("verifiers %s and %s", first, second)
	}
}

// SASLprep maps a password of only invisible characters to nothing; Postgres
// then treats it as prohibited and uses the raw bytes, never the empty string.
func TestVerifierKeepsAPasswordSASLprepEmpties(t *testing.T) {
	invisible, err := VerifierWithSalt("­​", fixedSalt())
	if err != nil {
		t.Fatal(err)
	}
	empty, err := VerifierWithSalt("", fixedSalt())
	if err != nil {
		t.Fatal(err)
	}
	if invisible == empty {
		t.Fatal("a password of invisible characters became the empty password")
	}
}
