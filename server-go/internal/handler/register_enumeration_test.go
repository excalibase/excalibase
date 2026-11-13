package handler

import (
	"net/http"
	"strings"
	"testing"
)

// A sign-up must answer the same whether the username or the address is
// already an account, or the form tells anyone which ones exist.
func TestRegister_TakenNameOrAddressAnswersLikeANewOne(t *testing.T) {
	base := postRegister(registerHandler(seededStore(), &inviteOrgStore{}, false), "bob", "bob@company.test")
	if base.Code != http.StatusCreated || !strings.Contains(base.Body.String(), "verification_required") {
		t.Fatalf("baseline sign-up: %d %s", base.Code, base.Body.String())
	}
	cases := map[string][2]string{
		"taken username": {"existing", "fresh@company.test"},
		"taken email":    {"fresh", "existing@x.test"},
	}
	for name, c := range cases {
		us := seededStore()
		before := len(us.users)
		w := postRegister(registerHandler(us, &inviteOrgStore{}, false), c[0], c[1])
		if len(us.users) != before {
			t.Errorf("%s: no account may be created for a taken name or address", name)
		}
		if w.Code != base.Code {
			t.Errorf("%s: status %d differs from a new sign-up's %d", name, w.Code, base.Code)
		}
		if strings.Contains(w.Body.String(), "taken") || strings.Contains(w.Body.String(), "registered") {
			t.Errorf("%s: the answer names the conflict: %s", name, w.Body.String())
		}
	}
}

// Without a live invite an invite-only platform refuses before it says anything about accounts.
func TestRegister_InviteOnlyRefusesBeforeRevealingATakenName(t *testing.T) {
	h := registerHandler(seededStore(), &inviteOrgStore{}, true)
	if w := postRegister(h, "existing", "fresh@company.test"); w.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", w.Code)
	}
}
