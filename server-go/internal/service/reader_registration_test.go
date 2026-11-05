package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// EXC-675: a project registered while dbreadrole is on gets the SELECT-only
// login, filed beside the platform's; one registered while it is off gets
// none, and a reader a restored copy brought cannot sign in.

func registeredStatements(h *registrationHarness) string {
	return strings.Join(h.kube.ExecCommands, "\n")
}

func TestRegistrationFilesTheReaderWhileItsFeatureIsOn(t *testing.T) {
	h := newRegistrationHarness(t)
	h.svc.SetFeatures(features.NewStatic(features.DBReadRole))
	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{ResetRolePasswords: true}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	record, err := h.vault.Get(vaultCredentialPath(testRegProject, roleReader))
	if err != nil || record["username"] != roleReader || len(record["password"]) < 32 {
		t.Fatalf("reader record %v, %v", record, err)
	}
	sql := registeredStatements(h)
	if !strings.Contains(sql, "excalibase_reader_refuses_ddl") || strings.Contains(sql, "excalibase_reader_lock") {
		t.Error("the reader was not created")
	}
	if strings.Contains(sql, record["password"]) {
		t.Error("the reader's password reached the statement text")
	}
	if !strings.Contains(sql, "ALTER ROLE %I WITH LOGIN PASSWORD %L', "+sqlTextLiteral(roleReader)) {
		t.Error("a restored copy's reader keeps the source's password")
	}
	if ok, err := h.svc.HasReaderLogin(testRegProject); !ok || err != nil {
		t.Errorf("HasReaderLogin = %v, %v", ok, err)
	}
}

func TestRegistrationWithoutTheFeatureLocksAnyReader(t *testing.T) {
	h := newRegistrationHarness(t)
	if err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{ResetRolePasswords: true}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	if _, err := h.vault.Get(vaultCredentialPath(testRegProject, roleReader)); err == nil {
		t.Fatal("a reader was filed with the feature off")
	}
	sql := registeredStatements(h)
	if !strings.Contains(sql, "excalibase_reader_lock") || strings.Contains(sql, "excalibase_reader_refuses_ddl") {
		t.Error("an unfiled reader is not locked")
	}
	if ok, _ := h.svc.HasReaderLogin(testRegProject); ok {
		t.Error("HasReaderLogin reports a reader that was never filed")
	}
}

func TestReaderSQLNamesTheOwnerAndAppAsCreators(t *testing.T) {
	sql := pgroles.ReaderRoleSQL("shop_owner", "pw")
	if !strings.Contains(sql, "ARRAY["+sqlTextLiteral("shop_owner")+", 'excalibase_app']") {
		t.Errorf("creators are not the owner and the app role:\n%s", sql)
	}
	if strings.Contains(sql, "'pw'") {
		t.Error("the password reached the statement as text")
	}
}
