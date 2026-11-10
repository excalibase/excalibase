package service

import (
	"context"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// The tests here drive production code and read what it actually sent.

var hexVerifierInStatement = regexp.MustCompile(hex.EncodeToString([]byte("SCRAM-SHA-256$")) + `[0-9a-f]+`)

// echoVerifierAsError answers like a psql whose failure quotes the statement,
// in the decoded form and in the hex form.
func echoVerifierAsError(stdin string) (string, error) {
	encoded := hexVerifierInStatement.FindString(stdin)
	if encoded == "" {
		return "", nil
	}
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return "", errors.New(`CONTEXT:  SQL statement "ALTER ROLE x PASSWORD '` + string(decoded) + `'" from decode('` + encoded + `')`)
}

func assertNoVerifierText(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, "SCRAM-SHA-256$") || strings.Contains(strings.ToLower(text), hex.EncodeToString([]byte("SCRAM-SHA-256$"))) {
		t.Errorf("%s holds a SCRAM verifier", where)
	}
}

func statementsSettingPasswords(stdins []string) []string {
	var found []string
	for _, stdin := range stdins {
		if strings.Contains(stdin, "PASSWORD %L") {
			found = append(found, stdin)
		}
	}
	return found
}

func TestRotationSilencesTheServerLogBeforeItNamesAVerifier(t *testing.T) {
	h := newRotationHarness(t)
	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	statements := statementsSettingPasswords(h.kube.ExecStdin)
	if len(statements) == 0 {
		t.Fatal("no statement set a password")
	}
	for _, statement := range statements {
		quiet, verifier := strings.Index(statement, pgroles.QuietSessionSQL()), strings.Index(statement, "PASSWORD %L")
		if quiet < 0 || quiet > verifier {
			t.Error("a rotation names a verifier before silencing the server log")
		}
	}
}

func TestRotationScrubsTheVerifierFromItsError(t *testing.T) {
	h := newRotationHarness(t)
	inst, _ := h.store.FindByProjectID(rotProject)
	h.kube.ExecAnswerStdin = echoVerifierAsError
	err := h.svc.alterRolePassword(context.Background(), inst, roleApp, "Some-Rotated-Password-1")
	if err == nil {
		t.Fatal("the failure was lost")
	}
	assertNoVerifierText(t, "the rotation's error", err.Error())
}

func TestReapplyScrubsTheVerifierFromItsError(t *testing.T) {
	h := newRotationHarness(t)
	inst, _ := h.store.FindByProjectID(rotProject)
	h.kube.ExecAnswerStdin = echoVerifierAsError
	err := h.svc.ReapplyCredentials(context.Background(), inst)
	if err == nil {
		t.Fatal("the failure was lost")
	}
	assertNoVerifierText(t, "the reapply's error", err.Error())
}

func TestMongoUserStatementsThatNameAVerifierAreQuietAndDropTheirContext(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatalf("CreateMongoUser: %v", err)
	}
	if _, err := h.svc.RotateMongoUser(ctx, mongoProject, "reporting"); err != nil {
		t.Fatalf("RotateMongoUser: %v", err)
	}
	statements := statementsSettingPasswords(h.kube.ExecStdin)
	if len(statements) != 2 {
		t.Fatalf("statements naming a verifier = %d, want create and rotate", len(statements))
	}
	for _, statement := range statements {
		if !strings.Contains(statement, pgroles.QuietSessionSQL()) {
			t.Error("a Mongo user statement names a verifier without silencing the server log")
		}
		if !strings.Contains(statement, raiseWithoutContext) {
			t.Error("a Mongo user block keeps the context that quotes its verifier")
		}
	}
}

// A drop names no verifier, so its error keeps the context an operator reads.
func TestMongoUserDropKeepsItsContext(t *testing.T) {
	if strings.Contains(dropMongoUserSQL("reporting"), raiseWithoutContext) {
		t.Error("the drop statement throws away its error context for nothing")
	}
}

func TestMongoUserFailureScrubsTheVerifier(t *testing.T) {
	h := newMongoUsersHarness(t)
	h.kube.ExecAnswerStdin = echoVerifierAsError
	_, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead)
	if err == nil {
		t.Fatal("the failure was lost")
	}
	assertNoVerifierText(t, "the Mongo user's error", err.Error())
}

// A FATAL cannot be caught in PL/pgSQL, so what psql printed can hold it.
func TestRoleSQLScrubsTheVerifierFromWhatItReturns(t *testing.T) {
	verifier := "SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk:c2VydmVy"
	failure := errors.New(`FATAL:  terminating connection CONTEXT: "PASSWORD '` + verifier + `'" decode('` + hex.EncodeToString([]byte(verifier)) + `')`)
	e := &scriptedExecer{outputs: []string{"160004", ""}, errs: []error{nil, failure}}
	err := runSuperuserRoleSQL(context.Background(), e.exec, "app", "SELECT 1;")
	if err == nil || !strings.Contains(err.Error(), "terminating connection") {
		t.Fatalf("the failure was lost: %v", err)
	}
	assertNoVerifierText(t, "the returned error", err.Error())
}

func TestRoleSQLThatNeverReachedItsEndIsAFailure(t *testing.T) {
	e := &scriptedExecer{cutShort: true, outputs: []string{"160004"}}
	if err := runSuperuserRoleSQL(context.Background(), e.exec, "app", "SELECT 1;"); err == nil {
		t.Fatal("exit 0 without the end marker reported success")
	}
}
