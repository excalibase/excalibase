package routepolicy

import "testing"

func TestHiddenRowsAnswerEveryoneButAPlatformAdminSessionAsMissing(t *testing.T) {
	row := Row{Auth: AuthSession, Permission: permManageOrgs, Unrestricted: true, HiddenFromOthers: true}
	cases := []struct {
		name string
		p    Principal
		want Expectation
	}{
		{"anonymous", Principal{Anonymous: true}, Deny401},
		{"customer", Principal{OrgRole: "owner"}, Deny404},
		{"admin session", Principal{PlatformAdmin: true}, Allow},
		{"admin read-only token", Principal{PlatformAdmin: true, ReadOnly: true, Restricted: true}, Deny404},
		{"admin narrowed token", Principal{PlatformAdmin: true, Restricted: true}, Deny404},
	}
	for _, tc := range cases {
		if got := Expect(row, "GET", tc.p); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}
