//go:build integration

package service

import (
	"context"
	"testing"
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
	mustExec(t, c.app, `SELECT excalibase.create_role('sneaky', 'pw', true)`)
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
