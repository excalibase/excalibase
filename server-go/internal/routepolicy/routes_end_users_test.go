package routepolicy

import (
	"net/http"
	"testing"
)

// EXC-662: reading, changing and deleting one end user is an Admin's, as the
// rest of end-user management is.
func TestEndUserAccountRows(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	developer := Principal{Name: "developer", OrgRole: "developer"}
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		row, ok := index[Key{Method: method, Pattern: "/api/projects/{projectId}/end-users/{userId}"}]
		if !ok || row.MinRole != roleAdmin || row.Param != ParamProject || row.Auth != AuthSession || row.Owner != OwnerProjectAccess {
			t.Errorf("%s: %+v", method, row)
			continue
		}
		if got := Expect(row, method, developer); got != Deny403 {
			t.Errorf("a Developer on %s: %s", method, got)
		}
	}
}
