package handler

import (
	"net/http"
	"strings"
	"testing"
)

// EXC-426: the engine and auth fetch a tenant's database credentials here.
// For a project created without a database they are told exactly that — a
// 409 they can pass on — rather than a 404 that reads as "no such project".
func TestVaultSecretRouteSaysAProjectHasNoDatabase(t *testing.T) {
	h, instances := vaultHandlerWithProject(t, "proj-1", "ACTIVE")
	instances.Items["proj-1"].NoDatabase = true

	for _, role := range []string{"excalibase_app", "auth_admin", "admin"} {
		w := getVaultSecret(t, h, "projects/proj-1/credentials/"+role)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "project has no database") {
			t.Errorf("%s: got %d %s, want 409 project has no database", role, w.Code, w.Body.String())
		}
	}

	// Secrets that are not database logins are answered as before.
	instances.Items["proj-1"].NoDatabase = true
	if w := getVaultSecret(t, h, "projects/proj-1/credentials/jwt_keys/anon_token"); w.Code == http.StatusConflict {
		t.Errorf("a non-database secret was refused as a database login: %d", w.Code)
	}
}

// EXC-832: a paused project's database is shut down, so the engine is told
// 423 and can answer "project paused" instead of failing to connect.
func TestVaultSecretRouteSaysAProjectIsPaused(t *testing.T) {
	h, _ := vaultHandlerWithProject(t, "proj-1", "PAUSED")

	for _, role := range []string{"excalibase_app", "auth_admin", "admin"} {
		w := getVaultSecret(t, h, "projects/proj-1/credentials/"+role)
		if w.Code != http.StatusLocked || !strings.Contains(w.Body.String(), "project is paused") {
			t.Errorf("%s: got %d %s, want 423 project is paused", role, w.Code, w.Body.String())
		}
	}

	if w := getVaultSecret(t, h, "projects/proj-1/credentials/jwt_keys/anon_token"); w.Code == http.StatusLocked {
		t.Errorf("a non-database secret was refused as a paused database login: %d", w.Code)
	}
}
