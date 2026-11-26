package service

import (
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/pgscram"
)

func mustVerifier(t *testing.T, password string) string {
	t.Helper()
	verifier, err := pgscram.Verifier(password)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

// EXC-821: a role whose stored verifier is not the vault's password was
// changed in SQL; only that role is repaired.
func TestPasswordDriftNamesOnlyTheRolesThatDiffer(t *testing.T) {
	stored := []storedRoleVerifier{
		{Role: "excalibase_app", Verifier: mustVerifier(t, "changed-in-sql")},
		{Role: "owner_app", Verifier: mustVerifier(t, "owner-pass")},
	}
	want := []RolePassword{
		{Role: "excalibase_app", Password: "vault-app-pass"},
		{Role: "owner_app", Password: "owner-pass"},
		{Role: "excalibase_reader", Password: "not-in-database"},
	}
	got := passwordDrift(stored, want)
	if !reflect.DeepEqual(got, []RolePassword{{Role: "excalibase_app", Password: "vault-app-pass"}}) {
		t.Fatalf("drift = %+v, want only excalibase_app", got)
	}
}

func TestPasswordDriftTreatsAnUnreadableVerifierAsDrift(t *testing.T) {
	got := passwordDrift([]storedRoleVerifier{{Role: "excalibase_app", Verifier: ""}},
		[]RolePassword{{Role: "excalibase_app", Password: "vault-app-pass"}})
	if len(got) != 1 {
		t.Fatalf("a role with no SCRAM verifier was not reported: %+v", got)
	}
}
