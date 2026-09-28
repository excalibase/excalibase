package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// Scripts run as the superuser must not resolve an unqualified call to a
// function a customer created in public.
func TestSuperuserScriptsPinTheirSearchPath(t *testing.T) {
	spec := projectRoleSpec{databaseName: "app", adminUsername: "owner_x", resetPasswords: true}
	if sql := projectRoleSQL(spec, projectRoleCredentials{authPassword: "a", appPassword: "b", watcherPassword: "c"}, "pub"); !strings.HasPrefix(sql, pgroles.PinnedSearchPath) {
		t.Error("the registration script does not pin its search_path")
	}
	argv := documentDBPsql("postgres", dropAllMongoUsersSQL())
	if !strings.HasPrefix(argv[len(argv)-1], pgroles.PinnedSearchPath) {
		t.Error("the DocumentDB statements do not pin their search_path")
	}

	h := newMongoUsersHarness(t)
	if _, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatal(err)
	}
	for _, stdin := range h.kube.ExecStdin {
		if strings.Contains(stdin, "ROLE") && !strings.HasPrefix(stdin, pgroles.PinnedSearchPath) {
			t.Errorf("a Mongo user statement does not pin its search_path: %q", stdin)
		}
	}
}
