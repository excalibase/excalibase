//go:build integration

package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// EXC-761: a project registered before the role functions were closed to
// excalibase_app still lets customer SQL create LOGIN roles. The reconcile's
// probe finds it, and registering again takes the grants away; the owner keeps
// them, since role management runs as the owner's login.
func TestRoleFunctions_ReregistrationRevokesAnOlderProjectsAppGrants(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)

	mustFail(t, c.app, `SELECT excalibase.create_role('sneaky', 'pw', true)`, "permission denied")
	mustExec(t, c.owner(t), `SELECT excalibase.create_role('by_owner', NULL, false)`)

	// What an older registration left behind.
	mustExec(t, c.admin, `GRANT EXECUTE ON FUNCTION excalibase.create_role(text, text, boolean, text[]) TO excalibase_app;
		GRANT EXECUTE ON FUNCTION excalibase.alter_role_password(text, text) TO excalibase_app;
		GRANT EXECUTE ON FUNCTION excalibase.drop_role(text) TO excalibase_app`)
	// Even with the grant back, the function refuses a session that is not the owner's (EXC-765).
	mustFail(t, c.app, `SELECT excalibase.create_role('sneaky', 'pw', true)`, "only the project owner")
	mustExec(t, c.owner(t), `SELECT excalibase.create_role('sneaky', 'pw', true)`)
	ctx := context.Background()
	if pending, err := appReachesAuth(ctx, c.pg.execer(), "app"); err != nil || !pending {
		t.Fatalf("probe on the old grants = %v, %v; want pending", pending, err)
	}

	c.spec.resetPasswords = true
	c.register(t)

	mustFail(t, c.app, `SELECT excalibase.create_role('sneaky_two', 'pw', true)`, "permission denied")
	mustFail(t, c.app, `SELECT excalibase.alter_role_password('sneaky', 'pw')`, "permission denied")
	mustFail(t, c.app, `SELECT excalibase.drop_role('sneaky')`, "permission denied")
	mustExec(t, c.owner(t), `SELECT excalibase.drop_role('sneaky')`)
	if pending, err := appReachesAuth(ctx, c.pg.execer(), "app"); err != nil || pending {
		t.Fatalf("probe after re-registration = %v, %v; want not pending", pending, err)
	}
}

// EXC-765 review: a database moved from Postgres 15 to 16 keeps the runtime
// login's memberships as they were made there, with SET. The reconcile's probe
// finds them and the registration SQL, run again, takes SET away.
func TestRuntimeLogin_ProbeFindsAMembershipThatCanSetRole(t *testing.T) {
	for _, image := range []string{"postgres:16", "postgres:15"} {
		t.Run(image, func(t *testing.T) {
			c := newPlatformSchemaCaseOn(t, startRolePostgresImage(t, image))
			c.register(t)
			verifier, err := newScramVerifier("Runtime-Password-1")
			if err != nil {
				t.Fatal(err)
			}
			runtimeSQL := pgroles.RuntimeRoleSQL(customerOwner, verifier)
			mustExec(t, c.admin, runtimeSQL)
			ctx := context.Background()
			if reaches, err := appReachesAuth(ctx, c.pg.execer(), "app"); err != nil || reaches {
				t.Fatalf("probe on a fresh runtime login = %v, %v; want not pending", reaches, err)
			}
			if image == "postgres:15" {
				return
			}
			mustExec(t, c.admin, `GRANT excalibase_app TO excalibase_runtime WITH SET TRUE`)
			if reaches, err := appReachesAuth(ctx, c.pg.execer(), "app"); err != nil || !reaches {
				t.Fatalf("probe on a membership with SET = %v, %v; want pending", reaches, err)
			}
			mustExec(t, c.admin, runtimeSQL)
			if reaches, err := appReachesAuth(ctx, c.pg.execer(), "app"); err != nil || reaches {
				t.Fatalf("probe after the SQL ran again = %v, %v; want not pending", reaches, err)
			}
			// Locked, the login is no longer a path to anything.
			mustExec(t, c.admin, `GRANT excalibase_app TO excalibase_runtime WITH SET TRUE`)
			mustExec(t, c.admin, pgroles.RuntimeLockSQL())
			if reaches, err := appReachesAuth(ctx, c.pg.execer(), "app"); err != nil || reaches {
				t.Fatalf("probe on a locked login = %v, %v; want not pending", reaches, err)
			}
		})
	}
}
