package handler

import "testing"

// An invite goes only to an address that could register: the invite check
// and the sign-up check are one rule.
func TestInviteAddressFollowsTheRegistrationRule(t *testing.T) {
	for _, address := range []string{
		"name@example.com", "first.last+tag@sub.example.co.uk", "a_b%c-d@example.io",
		"a@b.c", `"a b"@example.com`, "user@[192.0.2.1]", "Name <name@example.com>",
		"a@example.com, b@example.com", "", "user@example",
	} {
		if got, want := isEmailAddress(address), isValidEmail(address); got != want {
			t.Errorf("%q: invite check %v, sign-up check %v", address, got, want)
		}
	}
	if !isEmailAddress("name@example.com") {
		t.Error("a plain address must be invitable")
	}
}
