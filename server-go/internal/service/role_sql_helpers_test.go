package service

import (
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/pgscram"
)

func mustProjectRoleSQL(t *testing.T, spec projectRoleSpec, creds projectRoleCredentials, publicationName string) string {
	t.Helper()
	sql, err := projectRoleSQL(spec, creds, publicationName)
	if err != nil {
		t.Fatalf("project role SQL: %v", err)
	}
	return sql
}

func mustRoleVerifiers(t *testing.T, roles []RolePassword) []RoleVerifier {
	t.Helper()
	verifiers, err := roleVerifiers(roles)
	if err != nil {
		t.Fatalf("role verifiers: %v", err)
	}
	return verifiers
}

// assertNoPassword names the role and the form a leak took, never the value.
func assertNoPassword(t *testing.T, where, text string, passwords map[string]string) {
	t.Helper()
	lower := strings.ToLower(text)
	for role, password := range passwords {
		if password == "" {
			continue
		}
		if strings.Contains(text, password) {
			t.Errorf("%s holds the %s password in clear", where, role)
		}
		if strings.Contains(lower, hex.EncodeToString([]byte(password))) {
			t.Errorf("%s holds the %s password as hex", where, role)
		}
	}
}

// assertSetsVerifierOf checks that sql sets role to the verifier of password:
// recomputing it with the same salt proves the filed password is the one set.
func assertSetsVerifierOf(t *testing.T, sql, role, password string) {
	t.Helper()
	statement := regexp.MustCompile(regexp.QuoteMeta("PASSWORD %L', "+sqlTextLiteral(role)+", convert_from(decode('") + "([0-9a-f]+)")
	match := statement.FindStringSubmatch(sql)
	if match == nil {
		t.Fatalf("no statement sets the password of %s", role)
	}
	decoded, err := hex.DecodeString(match[1])
	if err != nil {
		t.Fatalf("decode the value set on %s: %v", role, err)
	}
	fields := strings.Split(string(decoded), "$")
	if len(fields) != 3 || fields[0] != "SCRAM-SHA-256" {
		t.Fatalf("the value set on %s is not a SCRAM verifier", role)
	}
	salt, err := base64.StdEncoding.DecodeString(strings.SplitN(fields[1], ":", 2)[1])
	if err != nil {
		t.Fatalf("salt of %s: %v", role, err)
	}
	want, err := pgscram.VerifierWithSalt(password, salt)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != want {
		t.Errorf("the verifier set on %s is not of its filed password", role)
	}
}
