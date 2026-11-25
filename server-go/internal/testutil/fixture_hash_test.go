package testutil_test

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// A fixture hash that production code verifies like any other, at a cost small
// enough that dozens of parallel logins in a test do not hold gigabytes.
func TestFixtureVerifiableHashChecksOutAtATinyCost(t *testing.T) {
	password := testutil.FixturePassword("tiny")
	hash := testutil.FixtureVerifiableHash(password)

	if !auth.CheckPassword(password, hash) {
		t.Fatal("the right password did not verify against the fixture hash")
	}
	if auth.CheckPassword(password+"x", hash) {
		t.Fatal("a wrong password verified against the fixture hash")
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=") || strings.Contains(hash, "m=65536") {
		t.Fatalf("hash %q is not an argon2id hash at a reduced cost", hash)
	}
	if testutil.FixtureVerifiableHash(password) == hash {
		t.Fatal("two fixture hashes of one password share a salt")
	}
}
