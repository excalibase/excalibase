package handler

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/abuse"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// guardDNS answers like a small internet: a few real mail domains, a temp-mail host, and failures.
type guardDNS struct{}

func (guardDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	switch name {
	case "gmail.com", "company.test", "x.test", "example.com":
		return []*net.MX{{Host: "mx." + name + ".", Pref: 10}}, nil
	case "newthrowaway.test":
		return []*net.MX{{Host: "in.mail.tm.", Pref: 10}}, nil
	case "slow.test":
		return nil, &net.DNSError{Err: "i/o timeout", IsTimeout: true}
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (guardDNS) LookupHost(context.Context, string) ([]string, error) {
	return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
}

func newSignupGuardOn(perHour int) *abuse.SignupGuard {
	return abuse.NewSignupGuard(features.NewStatic(features.SignupGuard), abuse.NewEmailPolicy(guardDNS{}), perHour)
}

func guardedSignup(perHour int) *signupFixture {
	f := newSignupFixture(false)
	f.h.SetSignupGuard(newSignupGuardOn(perHour))
	return f
}

func TestRegister_RefusesDisposableAndTempMailDomains(t *testing.T) {
	cases := map[string]int{
		"bob@fldubai.com":            http.StatusBadRequest,
		"bob@sharklasers.com":        http.StatusBadRequest,
		"bob@Mail.Guerrillamail.COM": http.StatusBadRequest,
		"bob@newthrowaway.test":      http.StatusBadRequest,
		"bob@no-mail-here.test":      http.StatusBadRequest,
		"bob@slow.test":              http.StatusServiceUnavailable,
	}
	for address, want := range cases {
		f := guardedSignup(100)
		before := len(f.users.users)
		w := postRegister(f.h, "bob", address)
		if w.Code != want {
			t.Errorf("%s: status %d, want %d: %s", address, w.Code, want, w.Body.String())
		}
		if len(f.users.users) != before || len(f.queued) != 0 {
			t.Errorf("%s: an account or mail was made for a refused address", address)
		}
	}
}

func TestRegister_AllowsRealAddresses(t *testing.T) {
	for _, address := range []string{"bob@gmail.com", "bob@company.test"} {
		f := guardedSignup(100)
		if w := postRegister(f.h, "bob", address); w.Code != http.StatusCreated {
			t.Errorf("%s: %d %s", address, w.Code, w.Body.String())
		}
	}
}

func TestRegister_OffFlagAcceptsADisposableAddress(t *testing.T) {
	f := newSignupFixture(false)
	f.h.SetSignupGuard(abuse.NewSignupGuard(features.NewStatic(), abuse.NewEmailPolicy(guardDNS{}), 1))
	if w := postRegister(f.h, "bob", "bob@fldubai.com"); w.Code != http.StatusCreated {
		t.Fatalf("flag off: %d %s", w.Code, w.Body.String())
	}
}

func TestRegister_LimitsSignUpsPerNetwork(t *testing.T) {
	f := guardedSignup(2)
	for _, name := range []string{"bob", "carol"} {
		if w := postRegister(f.h, name, name+"@gmail.com"); w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	w := postRegister(f.h, "dave", "dave@gmail.com")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("3rd sign-up: %d (Retry-After %q) %s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
}

func TestRegister_TheFirstAdminIsNeverRefusedByTheSignupGuard(t *testing.T) {
	h, raw := firstAdminHandler(t)
	h.SetSignupGuard(newSignupGuardOn(1))
	if w := postRegisterWithToken(h, "founder", "founder@fldubai.com", raw); w.Code != http.StatusCreated {
		t.Fatalf("first admin: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminCreateUser_RefusesADisposableAddress(t *testing.T) {
	us := newMockUserStore()
	h := NewAuthHandler(us, newMockTokenStore())
	h.SetSignupGuard(newSignupGuardOn(100))
	body := `{"username":"newuser","email":"new@tanpony.com","password":"Str0ng-Passw0rd-xyz","role":"platform_viewer"}`
	req := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.CreateUser(w, req)
	if w.Code != http.StatusBadRequest || len(us.users) != 0 {
		t.Fatalf("got %d, %d users: %s", w.Code, len(us.users), w.Body.String())
	}
}

func TestOAuth_RefusesANewAccountOnADisposableAddress(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.auth.SetSignupGuard(newSignupGuardOn(100))
	h.signIn.identity.Email = "dev@guerrillamailblock.com"
	before := len(h.users.users)

	w := h.callback("st4te")

	if got := redirectedTo(t, w).Query().Get("oauth_error"); got != "email_not_allowed" {
		t.Fatalf("oauth_error = %q", got)
	}
	if len(h.users.users) != before || sessionCookie(w) != nil {
		t.Error("an account or session was made for a refused address")
	}
}

func TestOAuth_FailsClosedWhenTheCheckCannotRun(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.auth.SetSignupGuard(newSignupGuardOn(100))
	h.signIn.identity.Email = "dev@slow.test"
	if got := redirectedTo(t, h.callback("st4te")).Query().Get("oauth_error"); got != "email_try_again" {
		t.Fatalf("oauth_error = %q", got)
	}
}

func TestOAuth_LimitsNewAccountsPerNetworkAndAllowsRealOnes(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.auth.SetSignupGuard(newSignupGuardOn(1))
	if got := redirectedTo(t, h.callback("st4te")); got.Path != "/oauth/complete" {
		t.Fatalf("first sign-in: %s", got)
	}
	h.signIn.identity.Subject, h.signIn.identity.Email = "9999", "other@gmail.com"
	if got := redirectedTo(t, h.callback("st4te")).Query().Get("oauth_error"); got != "signup_rate_limited" {
		t.Fatalf("oauth_error = %q", got)
	}
}

// A refused attempt spends nothing from the network's budget.
func TestRegister_RefusedAttemptsDoNotSpendTheBudget(t *testing.T) {
	f := guardedSignup(1)
	for _, address := range []string{"a@fldubai.com", "b@newthrowaway.test", "c@no-mail-here.test", "d@slow.test"} {
		if w := postRegister(f.h, "bob", address); w.Code == http.StatusCreated || w.Code == http.StatusTooManyRequests {
			t.Fatalf("%s: %d", address, w.Code)
		}
	}
	if w := postRegister(f.h, "bob", "bob@gmail.com"); w.Code != http.StatusCreated {
		t.Fatalf("a real sign-up after four refusals: %d %s", w.Code, w.Body.String())
	}
	if w := postRegister(f.h, "carol", "carol@gmail.com"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the budget of one was not spent by the real sign-up: %d", w.Code)
	}
}

// A live invite names the address it is for, so accepting one never spends the budget.
func TestRegister_AValidInviteIsNotLimitedByTheNetworkBudget(t *testing.T) {
	const token = "invite-token"
	f := guardedSignup(1)
	f.h.SetOrgStore(&inviteOrgStore{tokenHash: hashToken(token), email: "bob@company.test"})
	if w := postRegister(f.h, "alice", "alice@gmail.com"); w.Code != http.StatusCreated {
		t.Fatalf("budget spend: %d", w.Code)
	}
	if w := postRegisterWithInvite(f.h, "bob", "bob@company.test", token); w.Code != http.StatusCreated {
		t.Fatalf("invited sign-up after the budget is spent: %d %s", w.Code, w.Body.String())
	}
}

func TestRegister_AnInviteDoesNotExemptADisposableAddress(t *testing.T) {
	const token = "invite-token"
	f := guardedSignup(5)
	f.h.SetOrgStore(&inviteOrgStore{tokenHash: hashToken(token), email: "bob@fldubai.com"})
	if w := postRegisterWithInvite(f.h, "bob", "bob@fldubai.com", token); w.Code != http.StatusBadRequest {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}

func TestOAuth_RefusedAttemptsDoNotSpendTheBudget(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.auth.SetSignupGuard(newSignupGuardOn(1))
	h.signIn.identity.Email = "dev@fldubai.com"
	for range 3 {
		if got := redirectedTo(t, h.callback("st4te")).Query().Get("oauth_error"); got != "email_not_allowed" {
			t.Fatalf("oauth_error = %q", got)
		}
	}
	h.signIn.identity.Email = "dev@gmail.com"
	if got := redirectedTo(t, h.callback("st4te")); got.Path != "/oauth/complete" {
		t.Fatalf("a real sign-up after refusals: %s", got)
	}
}
