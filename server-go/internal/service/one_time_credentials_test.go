package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const chosenOwnerPassword = "correct horse battery staple"

func oneTimeHarness(t *testing.T) *rotationHarness {
	t.Helper()
	h := newRotationHarness(t)
	h.svc.SetFeatures(features.NewStatic(features.OneTimeCredentials))
	return h
}

func credentialsJSON(t *testing.T, h *rotationHarness) map[string]any {
	t.Helper()
	creds, err := h.svc.GetCredentials(context.Background(), rotProject)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	encoded, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestCredentialsHaveNoPasswordFieldWhileOneTimeIsOn(t *testing.T) {
	h := oneTimeHarness(t)
	h.svc.SetFeatures(features.NewStatic(features.OneTimeCredentials, features.DBReadRole))
	h.vault.data["projects/rot-db/credentials/excalibase_reader"] = map[string]string{"password": "reader-secret", "username": "excalibase_reader"}
	body := credentialsJSON(t, h)
	for key := range body {
		if strings.Contains(strings.ToLower(key), "password") {
			t.Errorf("the credentials response has a %q field", key)
		}
	}
	encoded, _ := json.Marshal(body)
	for _, secret := range []string{testutil.FixturePassword(rotOwner), "reader-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("the credentials response carries a stored password: %s", encoded)
		}
	}
	if url, _ := body["connectionUrl"].(string); !strings.Contains(url, "<password>") {
		t.Errorf("connection URL = %q, want a template with <password>", url)
	}
}

func TestCredentialsStillCarryThePasswordWhileOneTimeIsOff(t *testing.T) {
	h := newRotationHarness(t)
	body := credentialsJSON(t, h)
	if body["password"] != testutil.FixturePassword(rotOwner) {
		t.Errorf("flag off must keep the old shape, got %v", body["password"])
	}
}

func TestRotateReturnsTheNewPasswordOnceAndGetNeverDoes(t *testing.T) {
	h := oneTimeHarness(t)
	rotated, err := h.svc.RotateCredentials(context.Background(), rotProject)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.Password == "" || rotated.Password != h.currentOwnerPassword(t) {
		t.Fatalf("rotate returned %q, vault holds %q", rotated.Password, h.currentOwnerPassword(t))
	}
	if rotated.ReaderPassword != "" || strings.Contains(rotated.ConnectionURL, rotated.Password) {
		t.Errorf("rotate response leaks beyond the one owner password: %+v", rotated)
	}
	if again := credentialsJSON(t, h); again["password"] != nil {
		t.Errorf("a later read returned the password: %v", again["password"])
	}
}

func TestRotateToAChosenPasswordFilesAndReturnsIt(t *testing.T) {
	h := oneTimeHarness(t)
	rotated, err := h.svc.RotateCredentialsTo(context.Background(), rotProject, chosenOwnerPassword)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.Password != chosenOwnerPassword || h.currentOwnerPassword(t) != chosenOwnerPassword {
		t.Errorf("returned %q, vault %q, want the chosen password", rotated.Password, h.currentOwnerPassword(t))
	}
}

func TestAChosenPasswordMustBeStrong(t *testing.T) {
	for _, weak := range []string{"short", "aaaaaaaaaaaaaaaaaaaa", strings.Repeat("ab", 70), "with\x00nul-and-long-enough", "=starts-with-equals-sign", "+starts-with-plus-sign-x", "-starts-with-a-dash-xyz", "@starts-with-at-sign-xyz"} {
		if err := ValidateDatabasePassword(weak); err == nil {
			t.Errorf("%q was accepted", weak)
		}
	}
	for _, ok := range []string{chosenOwnerPassword, "Tr0ub4dor&3-and-more", "0123456789abcdef-xyz"} {
		if err := ValidateDatabasePassword(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}

func TestACreatedProjectIsFiledUnderThePasswordItWasCreatedWith(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	mock.AutoReconcileClusters = true
	v := newFakeVault()
	svc.SetVault(v)
	svc.SetFeatures(features.NewStatic(features.OneTimeCredentials))
	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: "chosen", OrgID: "org1", DBType: domain.PostgreSQL, OwnerPassword: chosenOwnerPassword,
	})
	if err != nil || resp.Status == "FAILED" {
		t.Fatalf("Provision: %v %+v", err, resp)
	}
	if got := v.data["projects/"+resp.ProjectID+"/credentials/admin"]["password"]; got != chosenOwnerPassword {
		t.Errorf("the vault holds %q for the owner, want the chosen password", got)
	}
	scram := false
	for _, stdin := range mock.ExecStdin {
		if strings.Contains(stdin, "ALTER ROLE %I WITH LOGIN PASSWORD") {
			scram = true
		}
		if strings.Contains(stdin, chosenOwnerPassword) {
			t.Errorf("the plaintext password reached a statement")
		}
	}
	if !scram {
		t.Error("the database was never told the chosen password")
	}
}

func TestAChosenPasswordNeverReplacesAnUnfinishedReset(t *testing.T) {
	h := oneTimeHarness(t)
	h.vault.data[rotAdminPending] = map[string]string{"password": "left-by-an-interrupted-reset", "username": rotOwner}
	_, err := h.svc.RotateCredentialsTo(context.Background(), rotProject, chosenOwnerPassword)
	if !errors.Is(err, ErrResetUnfinished) {
		t.Fatalf("err = %v, want ErrResetUnfinished", err)
	}
	if h.vault.data[rotAdminPending]["password"] != "left-by-an-interrupted-reset" {
		t.Error("the pending record was overwritten")
	}
}

func TestAGeneratedOwnerPasswordNeverStartsWithAFormulaCharacter(t *testing.T) {
	for range 200 {
		h := oneTimeHarness(t)
		rotated, err := h.svc.RotateCredentials(context.Background(), rotProject)
		if err != nil {
			t.Fatalf("rotate: %v", err)
		}
		if strings.ContainsRune("=+-@", rune(rotated.Password[0])) {
			t.Fatalf("generated password %q starts with a formula character", rotated.Password)
		}
	}
}
