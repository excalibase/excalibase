package handler

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
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

// A taken name on the very first registration is a plain conflict: only a setup-token holder gets that far.
func TestRegister_FirstRegistrationConflictIsReported(t *testing.T) {
	us := newMockUserStore()
	us.users["svc"] = &domain.User{ID: "svc", Username: "taken", Email: "svc@svc.excalibase.internal", Kind: domain.UserKindService}
	h := registerHandler(us, &inviteOrgStore{}, false)
	if w := postRegisterWithToken(h, "taken", "me@x.test", "setup-token"); w.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", w.Code)
	}
}

func TestNotifyAccountExists_FailuresSurface(t *testing.T) {
	ctx := context.Background()
	if err := NewEmailVerifier(newFakeVerificationStore(), email.NewNoopSender(), testStudioURL, "").NotifyAccountExists(ctx, "a@x.test", false); !errors.Is(err, ErrVerificationUnavailable) {
		t.Fatalf("got %v, want ErrVerificationUnavailable", err)
	}
	sender := &recordingSender{err: errors.New("provider down")}
	if err := NewEmailVerifier(newFakeVerificationStore(), sender, testStudioURL, "").NotifyAccountExists(ctx, "a@x.test", true); err == nil {
		t.Fatal("a failed send must surface")
	}
}

func TestRegister_FailedAccountExistsMailIsOnlyLogged(t *testing.T) {
	f := newSignupFixture(false)
	f.sender.err = errors.New("provider down")
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	before := len(f.users.users)

	if w := postRegister(f.h, "existing", "fresh@company.test"); w.Code != http.StatusCreated {
		t.Fatalf("got %d", w.Code)
	}
	f.runQueued()

	if !strings.Contains(logged.String(), "sign-up notice") || !strings.Contains(logged.String(), "provider down") {
		t.Errorf("the failed notice was not logged with its cause: %q", logged.String())
	}
	if len(f.users.users) != before || len(f.sender.sent) != 0 || len(f.queued) != 0 {
		t.Errorf("a failed notice changed something: %d accounts (was %d), %d sent, %d queued", len(f.users.users), before, len(f.sender.sent), len(f.queued))
	}
}

// One notice per address per hour, so the form cannot be used to mail-bomb someone.
func TestRegister_NoticeMailIsLimitedPerAddress(t *testing.T) {
	f := newSignupFixture(false)
	clock := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	f.h.verifier.now = func() time.Time { return clock }
	attempt := func(address string) {
		if w := postRegister(f.h, "existing", address); w.Code != http.StatusCreated {
			t.Fatalf("got %d", w.Code)
		}
		f.runQueued()
	}
	for i := 0; i < 5; i++ {
		attempt("Victim@company.test")
	}
	if len(f.sender.sent) != 1 {
		t.Fatalf("%d notices to one address inside an hour, want 1", len(f.sender.sent))
	}
	attempt("other@company.test")
	if len(f.sender.sent) != 2 {
		t.Fatalf("another address got no notice: %d sent", len(f.sender.sent))
	}
	clock = clock.Add(time.Hour + time.Minute)
	attempt("victim@company.test")
	if len(f.sender.sent) != 3 {
		t.Fatalf("no notice after the hour: %d sent", len(f.sender.sent))
	}
}

// A failed send must not use up the address's hour: the next attempt tries again.
func TestRegister_FailedNoticeDoesNotSpendTheHour(t *testing.T) {
	f := newSignupFixture(false)
	f.sender.err = errors.New("provider down")
	postRegister(f.h, "existing", "victim@company.test")
	f.runQueued()
	f.sender.err = nil
	postRegister(f.h, "existing", "victim@company.test")
	f.runQueued()
	if len(f.sender.sent) != 1 {
		t.Fatalf("%d sent, want 1 after the provider recovered", len(f.sender.sent))
	}
}

// An invite spent by an account that is then removed because its mail could not
// be sent goes back to pending, so the invited person can try again.
func TestRegister_FailedVerificationMailRestoresTheInvite(t *testing.T) {
	const token = "invite-token"
	f := newSignupFixture(true)
	org := &inviteOrgStore{tokenHash: hashToken(token)}
	f.h.SetOrgStore(org)
	f.sender.err = errors.New("provider down")

	if w := postRegisterWithInvite(f.h, "bob", "bob@company.test", token); w.Code != http.StatusCreated {
		t.Fatalf("got %d", w.Code)
	}
	if len(org.accepted) != 1 {
		t.Fatalf("the invite was not spent by the sign-up: %v", org.accepted)
	}
	f.runQueued()

	if len(f.users.users) != 1 {
		t.Fatalf("%d accounts, want only the seeded one", len(f.users.users))
	}
	if len(org.created) != 1 {
		t.Fatalf("the invite was restored %d times, want 1", len(org.created))
	}
	restored := org.created[0]
	if restored.TokenHash != hashToken(token) || restored.OrgID != "org1" || restored.Role != "developer" || restored.Email != "bob@company.test" || restored.ExpiresAt == nil {
		t.Fatalf("restored invite %+v does not match the spent one", restored)
	}
}

// With a sent mail the invite stays spent.
func TestRegister_SentVerificationMailKeepsTheInviteSpent(t *testing.T) {
	const token = "invite-token"
	f := newSignupFixture(true)
	org := &inviteOrgStore{tokenHash: hashToken(token)}
	f.h.SetOrgStore(org)
	postRegisterWithInvite(f.h, "bob", "bob@company.test", token)
	f.runQueued()
	if len(org.created) != 0 {
		t.Fatalf("a delivered sign-up restored its invite: %+v", org.created)
	}
}
