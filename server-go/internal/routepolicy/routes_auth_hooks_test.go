package routepolicy

import (
	"net/http"
	"testing"
)

// EXC-662: any Developer sees a project's auth hooks; pointing one at a URL or
// generating the signing secret is an Admin's.
func TestAuthHookRows(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	writes := []Key{
		{Method: http.MethodPut, Pattern: "/api/projects/{projectId}/auth-hooks/{hook}"},
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/auth-hooks/secret"},
		{Method: http.MethodDelete, Pattern: "/api/projects/{projectId}/auth-hooks/secret"},
	}
	read := Key{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/auth-hooks/"}
	for _, key := range append([]Key{read}, writes...) {
		row, ok := index[key]
		want := roleAdmin
		if key == read {
			want = roleDeveloper
		}
		if !ok || row.MinRole != want || row.Param != ParamProject || row.Auth != AuthSession || row.Capability != "" {
			t.Errorf("%s: %+v", key, row)
		}
	}
	developer := Principal{Name: "developer", OrgRole: "developer"}
	for _, key := range writes {
		if got := Expect(index[key], key.Method, developer); got != Deny403 {
			t.Errorf("a Developer on %s: %s", key, got)
		}
	}
	if got := Expect(index[read], http.MethodGet, developer); got != Allow {
		t.Errorf("a Developer reading the hooks: %s", got)
	}
}
