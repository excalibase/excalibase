package auth

import "testing"

// EXC-761: the operator role is documented as read-only in Studio, so it must
// not be able to destroy a tenant through the API either.
func TestPlatformOperatorCannotDeleteOrGovern(t *testing.T) {
	for _, perm := range []Permission{PermDelete, PermManageOrgs, PermManageUsers, PermManageSetup} {
		if HasPermission("platform_operator", perm) {
			t.Errorf("platform_operator holds %s", perm)
		}
	}
	if !HasPermission("platform_operator", PermViewAny) {
		t.Error("the operator must still see every project")
	}
}
