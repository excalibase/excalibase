package routepolicy

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Sites read on the viewer rung and change on the developer rung, like apps,
// and every row binds {projectId} to the caller.
func TestSiteRowsSitOnTheAuthoringRung(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	want := map[Key]string{
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/sites/"}:                                      domain.OrgRoleViewer,
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/sites/"}:                                     domain.OrgRoleDeveloper,
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/sites/{siteId}/"}:                             domain.OrgRoleViewer,
		{Method: http.MethodPatch, Pattern: "/api/projects/{projectId}/sites/{siteId}/"}:                           domain.OrgRoleDeveloper,
		{Method: http.MethodDelete, Pattern: "/api/projects/{projectId}/sites/{siteId}/"}:                          domain.OrgRoleDeveloper,
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploy"}:                      domain.OrgRoleDeveloper,
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys"}:                      domain.OrgRoleViewer,
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys/{deployId}"}:           domain.OrgRoleViewer,
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys/{deployId}/rollback"}: domain.OrgRoleDeveloper,
	}
	for key, role := range want {
		row, ok := index[key]
		if !ok {
			t.Errorf("%s has no policy row", key)
			continue
		}
		if row.MinRole != role || row.Auth != AuthSession || row.Param != ParamProject || row.Owner != OwnerProjectAccess {
			t.Errorf("%s: row %+v, want %s bound to the project", key, row, role)
		}
		if row.Capability != "" || row.ServiceOnly {
			t.Errorf("%s: no service token may reach the site surface", key)
		}
	}
}
