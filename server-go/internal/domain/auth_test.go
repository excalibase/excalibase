package domain

import "testing"

func TestUserIsService(t *testing.T) {
	cases := []struct {
		name string
		user *User
		want bool
	}{
		{name: "service principal", user: &User{Kind: UserKindService}, want: true},
		{name: "human", user: &User{Kind: UserKindHuman}},
		// Rows written before the kind column exists read as human, so a
		// legacy account can never be mistaken for a service identity.
		{name: "legacy row with no kind", user: &User{}},
		{name: "unknown kind", user: &User{Kind: "robot"}},
		// A missing user must answer false rather than panic: callers reach
		// this on the "user not found" path of a store lookup.
		{name: "nil user", user: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.user.IsService(); got != tc.want {
				t.Fatalf("IsService() = %v, want %v", got, tc.want)
			}
		})
	}
}
