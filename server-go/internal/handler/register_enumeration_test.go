package handler

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// signupFixture runs a sign-up with its mail held back, so a test sees what the
// request itself did and what only the background send does.
type signupFixture struct {
	h      *AuthHandler
	users  *mockUserStore
	sender *recordingSender
	queued []func()
}

func newSignupFixture(inviteOnly bool) *signupFixture {
	f := &signupFixture{users: seededStore(), sender: &recordingSender{}}
	f.h = NewAuthHandler(f.users, newMockTokenStore())
	f.h.SetSetupTokenStore(newMockSetupTokenStore(f.users))
	f.h.SetOrgStore(&inviteOrgStore{})
	f.h.SetInviteOnly(inviteOnly)
	f.h.SetEmailVerifier(NewEmailVerifier(newFakeVerificationStore(), f.sender, testStudioURL, ""))
	f.h.SetBackground(func(task func()) { f.queued = append(f.queued, task) })
	return f
}

func (f *signupFixture) runQueued() {
	for _, task := range f.queued {
		task()
	}
	f.queued = nil
}

// A sign-up must answer the same whether the username or the address is already
// an account, and do the same work on the request: the mail goes out in the
// background in every case, and says what happened.
func TestRegister_TakenNameOrAddressLooksLikeANewSignUp(t *testing.T) {
	cases := map[string]struct {
		username, email, subject string
	}{
		"new":            {"bob", "bob@company.test", "Verify your email"},
		"taken username": {"existing", "fresh@company.test", "That username is taken"},
		"taken email":    {"fresh", "existing@x.test", "You already have an account"},
	}
	want := map[string]int{}
	for name, c := range cases {
		f := newSignupFixture(false)
		before := len(f.users.users)
		w := postRegister(f.h, c.username, c.email)
		if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), "verification_required") {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		for _, leak := range []string{"taken", "registered", "exists"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("%s: the answer says %q: %s", name, leak, w.Body.String())
			}
		}
		if len(f.sender.sent) != 0 || len(f.queued) != 1 {
			t.Errorf("%s: %d mails sent inside the request and %d queued, want 0 and 1", name, len(f.sender.sent), len(f.queued))
		}
		f.runQueued()
		if len(f.sender.sent) != 1 || f.sender.sent[0].Subject != c.subject || f.sender.sent[0].To[0] != c.email {
			t.Errorf("%s: mail %+v, want %q to %s", name, f.sender.sent, c.subject, c.email)
		}
		want[name] = len(f.users.users) - before
	}
	if want["new"] != 1 || want["taken username"] != 0 || want["taken email"] != 0 {
		t.Errorf("accounts created: %v, want only the new sign-up to make one", want)
	}
}

// The account made for a sign-up whose mail cannot be sent is removed again, so the address stays free.
func TestRegister_FailedVerificationMailRemovesTheAccount(t *testing.T) {
	f := newSignupFixture(false)
	f.sender.err = errors.New("provider down")
	if w := postRegister(f.h, "bob", "bob@company.test"); w.Code != http.StatusCreated {
		t.Fatalf("got %d", w.Code)
	}
	f.runQueued()
	if len(f.users.users) != 1 {
		t.Fatalf("%d accounts, want only the seeded one", len(f.users.users))
	}
}

// Without a live invite an invite-only platform refuses before it says anything about accounts.
func TestRegister_InviteOnlyRefusesBeforeRevealingATakenName(t *testing.T) {
	h := newSignupFixture(true).h
	if w := postRegister(h, "existing", "fresh@company.test"); w.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", w.Code)
	}
}
