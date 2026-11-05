package routepolicy

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// App git builds sit on the site git builds' rungs, bound to the caller's project; no service token reaches them.
func TestAppBuildRowsSitOnTheSiteBuildRungs(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	want := map[Key]string{
		{Method: http.MethodGet, Pattern: appBuildPath + "git"}:              domain.OrgRoleViewer,
		{Method: http.MethodPut, Pattern: appBuildPath + "git"}:              domain.OrgRoleDeveloper,
		{Method: http.MethodDelete, Pattern: appBuildPath + "git"}:           domain.OrgRoleDeveloper,
		{Method: http.MethodPost, Pattern: appBuildPath + "git/sign-in"}:     domain.OrgRoleDeveloper,
		{Method: http.MethodGet, Pattern: appBuildPath + "git/repositories"}: domain.OrgRoleDeveloper,
		{Method: http.MethodGet, Pattern: appBuildPath + "builds"}:           domain.OrgRoleViewer,
		{Method: http.MethodPost, Pattern: appBuildPath + "builds"}:          domain.OrgRoleDeveloper,
		{Method: http.MethodGet, Pattern: appBuildPath + "builds/{buildId}"}: domain.OrgRoleDeveloper,
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
			t.Errorf("%s: no service token may reach app builds", key)
		}
	}
}
