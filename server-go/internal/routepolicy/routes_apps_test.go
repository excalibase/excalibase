package routepolicy

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// The app surface reads on the viewer rung and is authored on the developer
// rung, and every row binds {projectId} to the caller. A row that named no
// owner would be a route any authenticated user could call against any
// tenant, which is the shape of the bugs this table exists to catch.
func TestAppRowsSitOnTheAuthoringRung(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	want := map[Key]string{
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/apps/"}:            domain.OrgRoleViewer,
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/apps/{appId}/"}:    domain.OrgRoleViewer,
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/apps/"}:           domain.OrgRoleDeveloper,
		{Method: http.MethodPatch, Pattern: "/api/projects/{projectId}/apps/{appId}/"}:  domain.OrgRoleDeveloper,
		{Method: http.MethodDelete, Pattern: "/api/projects/{projectId}/apps/{appId}/"}: domain.OrgRoleDeveloper,
	}
	for key, role := range want {
		row, ok := index[key]
		if !ok {
			t.Errorf("%s has no policy row", key)
			continue
		}
		if row.MinRole != role {
			t.Errorf("%s: min role %q, want %q", key, row.MinRole, role)
		}
		if row.Auth != AuthSession {
			t.Errorf("%s: auth %q, want %q", key, row.Auth, AuthSession)
		}
		if row.Param != ParamProject || row.Owner != OwnerProjectAccess {
			t.Errorf("%s: must bind {projectId} to the caller, got param=%q owner=%q", key, row.Param, row.Owner)
		}
		if row.Capability != "" || row.ServiceOnly {
			t.Errorf("%s: no service token may reach the app surface", key)
		}
	}
}
