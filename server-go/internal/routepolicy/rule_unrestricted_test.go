package routepolicy

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// unrestrictedExceptions are write rows owned by a platform role or by org
// membership that a narrowed token may still reach, with the reason.
var unrestrictedExceptions = map[string]string{}

// Every write that changes who holds authority, or what the platform runs, must
// refuse a narrowed token: a rule over the table, so a new row cannot forget it.
func TestEveryPlatformOrOrgWriteRefusesNarrowedTokens(t *testing.T) {
	for _, row := range Table() {
		if row.Owner != OwnerPlatformRole && row.Owner != OwnerOrgMembership {
			continue
		}
		if row.Auth != AuthSession {
			continue
		}
		for _, method := range row.expandMethods() {
			key := Key{Method: method, Pattern: row.Pattern}.String()
			if !isWrite(method) || row.Unrestricted {
				continue
			}
			if _, ok := unrestrictedExceptions[key]; !ok {
				t.Errorf("%s is a platform or org write that does not refuse narrowed tokens; mark it Unrestricted or name an exception", key)
			}
		}
	}
}

// A token bound to the project in the path is refused on the rows that make a project.
func TestBoundSameProjectIsRefusedWhereProjectsAreMade(t *testing.T) {
	boundHere := Principal{Name: "boundHere", OrgRole: domain.OrgRoleAdmin, Restricted: true, BoundSameProject: true}
	scopedWrite := Principal{Name: "scopedWrite", OrgRole: domain.OrgRoleAdmin, Restricted: true}
	create := Row{Pattern: "/c", Auth: AuthSession, Owner: OwnerNone, Unbound: true}
	copyRow := Row{Pattern: "/p/copy", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: domain.OrgRoleAdmin, Unbound: true}
	for _, row := range []Row{create, copyRow} {
		if got := Expect(row, "POST", boundHere); got != Deny403 {
			t.Errorf("%s bound: %s, want 403", row.Pattern, got)
		}
		if got := Expect(row, "POST", scopedWrite); got != Allow {
			t.Errorf("%s unbound write token: %s, want allow", row.Pattern, got)
		}
	}
}
