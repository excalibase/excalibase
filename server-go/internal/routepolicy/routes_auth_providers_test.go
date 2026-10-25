package routepolicy

import (
	"net/http"
	"testing"
)

// EXC-662: any Developer sees a project's sign-in providers; writing one, and
// with it a client secret, is an Admin's (ADR 0022).
func TestAuthProviderRows(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	for key, minRole := range map[Key]string{
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/auth-providers/"}:           roleDeveloper,
		{Method: http.MethodPut, Pattern: "/api/projects/{projectId}/auth-providers/{provider}"}: roleAdmin,
	} {
		row, ok := index[key]
		if !ok || row.MinRole != minRole || row.Param != ParamProject || row.Auth != AuthSession || row.Capability != "" {
			t.Errorf("%s: %+v", key, row)
		}
	}
	developer := Principal{Name: "developer", OrgRole: "developer"}
	put := index[Key{Method: http.MethodPut, Pattern: "/api/projects/{projectId}/auth-providers/{provider}"}]
	if got := Expect(put, http.MethodPut, developer); got != Deny403 {
		t.Errorf("a Developer writing a provider: %s", got)
	}
}
