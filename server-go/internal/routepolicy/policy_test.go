package routepolicy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestIndexCoversEveryRowMethod(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	want := 0
	for _, row := range Table() {
		want += len(row.expandMethods())
	}
	if len(index) != want {
		t.Fatalf("index holds %d routes, the table declares %d", len(index), want)
	}
}

func TestIndexRejectsADuplicateRoute(t *testing.T) {
	rows := []Row{
		{Methods: get, Pattern: "/x", Auth: AuthPublic},
		{Methods: get, Pattern: "/x", Auth: AuthSession},
	}
	if _, err := indexRows(rows); err == nil {
		t.Fatal("two rows claiming GET /x must be rejected")
	}
}

func TestMethodAllExpandsToEveryRegisteredMethod(t *testing.T) {
	row := Row{Methods: []string{MethodAll}, Pattern: "/x"}
	if got := row.expandMethods(); len(got) != len(allMethods) {
		t.Fatalf("MethodAll expanded to %v", got)
	}
	plain := Row{Methods: []string{http.MethodGet}, Pattern: "/x"}
	if got := plain.expandMethods(); len(got) != 1 || got[0] != http.MethodGet {
		t.Fatalf("explicit methods must pass through, got %v", got)
	}
}

func TestKeyString(t *testing.T) {
	if got := (Key{Method: http.MethodGet, Pattern: "/healthz"}).String(); got != "GET /healthz" {
		t.Fatalf("got %q", got)
	}
}

// TestEveryRowIsWellFormed pins the invariants that make a row readable: a
// pattern, a known authentication class, and a minimum role only where there
// is a tenant to hold that role in.
func TestEveryRowIsWellFormed(t *testing.T) {
	known := map[AuthClass]bool{
		AuthPublic: true, AuthSession: true, AuthHandlerSession: true,
		AuthRuntimeToken: true, AuthFunctionJWT: true,
	}
	for _, row := range Table() {
		if !strings.HasPrefix(row.Pattern, "/") {
			t.Errorf("row %q has no route pattern", row.Pattern)
		}
		if !known[row.Auth] {
			t.Errorf("%s declares unknown authentication class %q", row.Pattern, row.Auth)
		}
		if row.MinRole != "" && row.Param == ParamNone {
			t.Errorf("%s requires role %q but names no tenant to hold it in", row.Pattern, row.MinRole)
		}
		if row.ServiceOnly && row.Capability == "" {
			t.Errorf("%s is service-only but names no capability, so nothing could call it", row.Pattern)
		}
	}
}

// TestEveryProjectRouteBindsTheProject is the structural half of the
// cross-tenant guarantee: a route naming {projectId} must say what resolves
// the caller's right to that project.
func TestEveryProjectRouteBindsTheProject(t *testing.T) {
	for _, row := range Table() {
		if !strings.Contains(row.Pattern, "{"+ParamProject+"}") {
			continue
		}
		if row.Owner == OwnerNone {
			t.Errorf("%s names a project but declares no ownership resolution", row.Pattern)
		}
	}
}

var (
	anonymous  = Principal{Name: "anonymous", Anonymous: true}
	viewer     = Principal{Name: "viewer", OrgRole: domain.OrgRoleViewer}
	developer  = Principal{Name: "developer", OrgRole: domain.OrgRoleDeveloper}
	outsider   = Principal{Name: "outsider"}
	platformer = Principal{Name: "platformAdmin", PlatformAdmin: true}
	readOnly   = Principal{Name: "readOnly", PlatformAdmin: true, ReadOnly: true, Restricted: true}
	narrowed   = Principal{Name: "narrowed", PlatformAdmin: true, Restricted: true}
	elsewhere  = Principal{Name: "elsewhere", OrgRole: domain.OrgRoleAdmin, ForeignProject: true}
	capability = Principal{Name: "svc", PlatformAdmin: true, Capabilities: []string{"policies:read"}}
)

func TestExpect(t *testing.T) {
	projectWrite := Row{Pattern: "/p", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: domain.OrgRoleDeveloper}
	orgRead := Row{Pattern: "/o", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership}
	orgWrite := Row{Pattern: "/o", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership, MinRole: domain.OrgRoleAdmin}
	platform := Row{Pattern: "/a", Auth: AuthSession, Permission: permManageUsers, Owner: OwnerPlatformRole}
	unrestricted := Row{Pattern: "/a", Auth: AuthSession, Permission: permManageUsers, Owner: OwnerPlatformRole, Unrestricted: true}
	openRow := Row{Pattern: "/t", Auth: AuthSession, Owner: OwnerNone}
	withCapability := Row{Pattern: "/c", Auth: AuthSession, Owner: OwnerNone, Capability: "policies:read"}
	serviceOnly := Row{Pattern: "/s", Auth: AuthSession, Owner: OwnerNone, Capability: "email:send", ServiceOnly: true}

	cases := []struct {
		name   string
		row    Row
		method string
		who    Principal
		want   Expectation
	}{
		{"preflight is answered above routing", projectWrite, http.MethodOptions, anonymous, Unasserted},
		{"public needs nothing", Row{Auth: AuthPublic}, http.MethodGet, anonymous, Allow},
		{"no studio credential is an end-user JWT", Row{Auth: AuthFunctionJWT}, http.MethodPost, viewer, Deny401},
		{"an anonymous caller holds no end-user JWT either", Row{Auth: AuthFunctionJWT}, http.MethodGet, anonymous, Deny401},
		{"runtime secret is not held by any studio caller", Row{Auth: AuthRuntimeToken}, http.MethodPost, platformer, Deny401},
		{"capability token is default-deny", projectWrite, http.MethodGet, capability, Deny403},
		{"named capability is left to the service contract", withCapability, http.MethodGet, capability, Unasserted},
		{"anonymous on an authenticated route", projectWrite, http.MethodGet, anonymous, Deny401},
		{"handler-authenticated route skips the scope gate", Row{Auth: AuthHandlerSession}, http.MethodPost, readOnly, Allow},
		{"handler-authenticated route still refuses anonymous", Row{Auth: AuthHandlerSession}, http.MethodPost, anonymous, Deny401},
		{"service-only route refuses a session", serviceOnly, http.MethodPost, platformer, Deny403},
		{"a token bound elsewhere never leaves its project", projectWrite, http.MethodGet, elsewhere, Deny404},
		{"read-only token may not write", openRow, http.MethodPost, readOnly, Deny403},
		{"read-only token may still read", openRow, http.MethodGet, readOnly, Allow},
		{"narrowed credential may not mint authority", unrestricted, http.MethodPost, narrowed, Deny403},
		{"platform admin bypasses the org ladder", projectWrite, http.MethodPost, platformer, Allow},
		{"a tenant holds no platform permission", platform, http.MethodGet, developer, Deny403},
		{"a non-member must not learn the project exists", projectWrite, http.MethodGet, outsider, Deny404},
		{"a viewer may not author", projectWrite, http.MethodPost, viewer, Deny403},
		{"a developer may author", projectWrite, http.MethodPost, developer, Allow},
		{"an org read hides the org from outsiders", orgRead, http.MethodGet, outsider, Deny404},
		{"an org write answers on permission", orgWrite, http.MethodPost, outsider, Deny403},
		{"an org member may read", orgRead, http.MethodGet, viewer, Allow},
		{"an org viewer may not manage members", orgWrite, http.MethodPost, viewer, Deny403},
		{"a route with no tenant is open to any member", openRow, http.MethodGet, viewer, Allow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Expect(tc.row, tc.method, tc.who); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestIsCapabilityToken(t *testing.T) {
	if viewer.IsCapabilityToken() {
		t.Fatal("a session principal is not a capability token")
	}
	if !capability.IsCapabilityToken() {
		t.Fatal("a principal with permissions is a capability token")
	}
}

func TestIsWrite(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if isWrite(method) {
			t.Errorf("%s is a read", method)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if !isWrite(method) {
			t.Errorf("%s is a write", method)
		}
	}
}
