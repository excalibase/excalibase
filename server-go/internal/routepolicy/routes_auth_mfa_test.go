package routepolicy

import (
	"net/http"
	"testing"
)

// EXC-662: the MFA switch and an end user's factor reset are an Admin's.
func TestAuthMFARows(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	developer := Principal{Name: "developer", OrgRole: "developer"}
	for _, key := range []Key{
		{Method: http.MethodPut, Pattern: "/api/projects/{projectId}/auth-settings/mfa"},
		{Method: http.MethodDelete, Pattern: "/api/projects/{projectId}/end-users/{userId}/factors"},
	} {
		row, ok := index[key]
		if !ok || row.MinRole != roleAdmin || row.Param != ParamProject || row.Auth != AuthSession || row.Owner != OwnerProjectAccess || row.Capability != "" {
			t.Errorf("%s: %+v", key, row)
			continue
		}
		if got := Expect(row, key.Method, developer); got != Deny403 {
			t.Errorf("a Developer on %s: %s", key, got)
		}
	}
}
