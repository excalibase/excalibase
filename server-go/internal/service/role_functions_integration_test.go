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
	t.Run("postgres:16", func(t *testing.T) {
		c, runtimeSQL := runtimeProbeCase(t, "postgres:16")
		mustExec(t, c.admin, `GRANT excalibase_app TO excalibase_runtime WITH SET TRUE`)
		expectProbe(t, c, true, "a membership with SET")
		mustExec(t, c.admin, runtimeSQL)
		expectProbe(t, c, false, "after the SQL ran again")
		// Locked, the login is no longer a path to anything.
		mustExec(t, c.admin, `GRANT excalibase_app TO excalibase_runtime WITH SET TRUE`)
		mustExec(t, c.admin, pgroles.RuntimeLockSQL())
		expectProbe(t, c, false, "a locked login")
	})
	t.Run("postgres:15", func(t *testing.T) {
		runtimeProbeCase(t, "postgres:15")
	})
}

// runtimeProbeCase registers a project on the image, creates the runtime login
// and checks the probe reports nothing pending for it.
func runtimeProbeCase(t *testing.T, image string) (*platformSchemaCase, string) {
	t.Helper()
	c := newPlatformSchemaCaseOn(t, startRolePostgresImage(t, image))
	c.register(t)
	verifier, err := newScramVerifier("Runtime-Password-1")
	if err != nil {
		t.Fatal(err)
	}
	runtimeSQL := pgroles.RuntimeRoleSQL(customerOwner, verifier)
	mustExec(t, c.admin, runtimeSQL)
	expectProbe(t, c, false, "a fresh runtime login")
	return c, runtimeSQL
}

func expectProbe(t *testing.T, c *platformSchemaCase, want bool, when string) {
	t.Helper()
	if got, err := appReachesAuth(context.Background(), c.pg.execer(), "app"); err != nil || got != want {
		t.Fatalf("probe on %s = %v, %v; want %v", when, got, err, want)
	}
}
