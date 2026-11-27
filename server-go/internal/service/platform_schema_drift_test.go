package service

import (
	"reflect"
	"strings"
	"testing"
	"time"

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

// A tenant chooses the iteration count of the verifier it sets; one that is not
// the platform's is drift without ever being hashed (EXC-821).
func TestPasswordDriftNeverHashesACustomerChosenIterationCount(t *testing.T) {
	verifier := strings.Replace(mustVerifier(t, "vault-pass"), "$4096:", "$2000000000:", 1)
	start := time.Now()
	got := passwordDrift([]storedRoleVerifier{{Role: "excalibase_app", Verifier: verifier}},
		[]RolePassword{{Role: "excalibase_app", Password: "vault-pass"}})
	if len(got) != 1 {
		t.Fatalf("a forged iteration count was not reported as drift: %+v", got)
	}
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("the forged verifier was hashed: %s", took)
	}
}

func TestParsePlatformStateReadsBothFlagsAndTheVerifiers(t *testing.T) {
	state, err := parsePlatformState(probeAuthTag + "\tf\n" + probeSchemasTag + "\tt\nowner_app\tSCRAM-SHA-256$x\n")
	if err != nil {
		t.Fatal(err)
	}
	if state.appReachesAuth || !state.schemasDrifted || len(state.stored) != 1 || state.stored[0].Role != "owner_app" {
		t.Fatalf("state = %+v", state)
	}
	for _, bad := range []string{"", probeAuthTag + "\tf\n", probeSchemasTag + "\tf\n", probeAuthTag + "\tmaybe\n" + probeSchemasTag + "\tf\n"} {
		if _, err := parsePlatformState(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// A rotation in flight leaves the vault and the database apart on purpose: the
// password filed as pending, or a current record marked pending, is not drift.
func TestFiledPasswordsSkipARotationInFlight(t *testing.T) {
	h := newRotationHarness(t)
	roles := map[string]string{roleApp: roleApp, roleReader: roleReader, roleRuntime: roleRuntime}
	h.vault.data[vaultCredentialPath(rotProject, roleApp)] = map[string]string{"password": "app-pass"}
	h.vault.data[vaultCredentialPath(rotProject, roleReader)] = map[string]string{"password": "reader-pass"}
	h.vault.data[vaultPendingCredentialPath(rotProject, roleReader)] = map[string]string{"password": "reader-next"}
	h.vault.data[vaultCredentialPath(rotProject, roleRuntime)] = map[string]string{"password": "runtime-pass", "state": pendingCredentialState}

	got, err := h.svc.filedPasswords(rotProject, []string{roleApp, roleReader, roleRuntime}, roles)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []RolePassword{{Role: roleApp, Password: "app-pass"}}) {
		t.Fatalf("filed passwords = %+v, want only the role with no rotation in flight", got)
	}
}
