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
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/sites/limits"}:                                domain.OrgRoleViewer,
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/sites/{siteId}/tokens"}:                       domain.OrgRoleDeveloper,
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/sites/{siteId}/tokens"}:                      domain.OrgRoleDeveloper,
		{Method: http.MethodDelete, Pattern: "/api/projects/{projectId}/sites/{siteId}/tokens/{tokenId}"}:          domain.OrgRoleDeveloper,
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/sites/{siteId}/domains"}:                      domain.OrgRoleViewer,
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/sites/{siteId}/domains"}:                     domain.OrgRoleDeveloper,
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/sites/{siteId}/domains/{domainId}/refresh"}:  domain.OrgRoleDeveloper,
		{Method: http.MethodDelete, Pattern: "/api/projects/{projectId}/sites/{siteId}/domains/{domainId}"}:        domain.OrgRoleDeveloper,
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

// EXC-620: a site's deploy token reaches its deploy upload and deploy history,
// and no other row in the whole table names it.
func TestOnlyTheDeployRowsAdmitASiteDeployToken(t *testing.T) {
	want := map[Key]bool{
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploy"}:            true,
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys"}:            true,
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys/{deployId}"}: true,
	}
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	for key, row := range index {
		if row.SiteDeployToken != want[key] {
			t.Errorf("%s: SiteDeployToken = %v, want %v", key, row.SiteDeployToken, want[key])
		}
	}
	deployToken := Principal{Name: "siteDeployToken", OrgRole: domain.OrgRoleDeveloper, Restricted: true, SiteDeployToken: true}
	for key, row := range index {
		got := Expect(row, key.Method, deployToken)
		switch {
		case key.Method == http.MethodOptions:
		case want[key] && got != Allow:
			t.Errorf("%s: %s, want the deploy token allowed", key, got)
		case !want[key] && got != Deny403:
			t.Errorf("%s: %s, want the deploy token refused 403", key, got)
		}
	}
}
