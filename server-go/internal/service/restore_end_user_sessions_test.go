package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-635: only registering a project restored from a backup ends the
// sessions it brought; a new project and the startup reconcile of a live
// one leave its end users signed in.
func TestProjectRoleSQL_EndsTheSourcesSessionsOnlyForARestoredCopy(t *testing.T) {
	creds := projectRoleCredentials{authPassword: "a", appPassword: "b", watcherPassword: "c"}
	restored := &domain.DatabaseInstance{ProjectID: "copy", DatabaseName: "app", Username: "owner", RestoredFromProjectID: "src"}

	for name, tc := range map[string]struct {
		spec projectRoleSpec
		ends bool
	}{
		"restored copy":     {projectRoleSpec{databaseName: "app", adminUsername: "owner", restored: true}, true},
		"new project":       {projectRoleSpec{databaseName: "app", adminUsername: "owner"}, false},
		"reconcile of copy": {reconcileSpec(restored), false},
	} {
		t.Run(name, func(t *testing.T) {
			sql := projectRoleSQL(tc.spec, creds, "pub")
			if got := strings.Contains(sql, endUserSessionsRevokeSQL); got != tc.ends {
				t.Fatalf("ends sessions = %v, want %v", got, tc.ends)
			}
		})
	}
}

func TestEndUserSessionsRevokeSQL_KeepsTheUsers(t *testing.T) {
	for _, want := range []string{
		"DELETE FROM auth.refresh_tokens", "DELETE FROM auth.password_reset_tokens",
		"DELETE FROM auth.email_verification_tokens", "UPDATE auth.api_keys SET revoked_at",
	} {
		if !strings.Contains(endUserSessionsRevokeSQL, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(endUserSessionsRevokeSQL, "auth.users") {
		t.Error("the end users themselves must be kept")
	}
}

// The same on MySQL: a restored copy's registration ends the sessions, a new
// project's does not.
func TestMySQLRegistrationEndsTheSourcesSessionsOnlyForARestoredCopy(t *testing.T) {
	svc, store, engine := setupSingleHostMySQL(t)
	source := provisionMySQL(t, svc, store, "")
	if strings.Contains(engine.fed(), provisioner.MySQLEndUserSessionsRevokeSQL) {
		t.Fatal("a new project's registration ended sessions")
	}

	restored := *source
	restored.ProjectID, restored.OrgID, restored.RestoredFromProjectID, restored.CreatedAt = "copy-1", "org2", source.ProjectID, nil
	if err := svc.RegisterProject(context.Background(), &restored, RegistrationOptions{}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	if !strings.Contains(engine.fed(), provisioner.MySQLEndUserSessionsRevokeSQL) {
		t.Fatal("the restored copy kept the source's sessions")
	}
}

// A copy whose sessions could not be ended is not registered.
func TestMySQLRegistrationFailsWhenTheSourcesSessionsCannotBeEnded(t *testing.T) {
	svc, store, engine := setupSingleHostMySQL(t)
	source := provisionMySQL(t, svc, store, "")
	engine.answer = func(_, stdin string) (string, error) {
		if stdin == provisioner.MySQLEndUserSessionsRevokeSQL {
			return "", errors.New("exit code 1")
		}
		return "", nil
	}
	restored := *source
	restored.ProjectID, restored.OrgID, restored.RestoredFromProjectID, restored.CreatedAt = "copy-1", "org2", source.ProjectID, nil
	if err := svc.RegisterProject(context.Background(), &restored, RegistrationOptions{}); err == nil {
		t.Fatal("the copy was registered with the source's sessions")
	}
	if row, _ := store.FindByProjectID("copy-1"); row != nil {
		t.Fatalf("a row was left: %+v", row)
	}
}
