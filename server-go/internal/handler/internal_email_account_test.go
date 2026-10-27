package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/go-chi/chi/v5"
)

const testNewAddress = "renamed@example.com"

// EXC-662 step 4: account mails the auth service relays.
var accountMailBodies = map[string]string{
	"email_change":        `{"userEmail":"` + testNewAddress + `","linkUrl":"https://notes.acme.test/cb?token_hash=t9&type=email_change","expiresMin":"30"}`,
	"email_change_notice": `{"userEmail":"` + testRecipient + `","newEmail":"` + testNewAddress + `"}`,
	"identity_linked":     `{"userEmail":"` + testRecipient + `","method":"github","linkedEmail":""}`,
}

func postAccountMail(r http.Handler, template string) *httptest.ResponseRecorder {
	return postInternalEmail(r.(*chi.Mux), `{"projectId":"proj_p1","to":"`+testRecipient+`","template":"`+template+`","data":`+accountMailBodies[template]+`}`)
}

// accountMailRouter wires the relay; a nil source leaves project mail unwired.
func accountMailRouter(sender *recordingEmailSender, flags features.Flags, source *fakeSignInMail) *chi.Mux {
	r := chi.NewRouter()
	h := NewInternalEmailHandler(sender)
	if source != nil {
		h = h.WithSignInMail(flags, source)
	}
	h.Routes(r)
	return r
}

func oauthOnlyProject() *fakeSignInMail {
	source := signInMailOn()
	source.project.EmailOTPEnabled = false
	source.project.OAuthEnabled = true
	return source
}

func TestInternalEmailSend_AccountTemplatesAccepted(t *testing.T) {
	cases := map[string]struct {
		flags  features.Flags
		source *fakeSignInMail
	}{
		"email_change":        {features.NewStatic(features.AuthUser), signInMailOn()},
		"email_change_notice": {features.NewStatic(features.AuthUser), signInMailOn()},
		// Only Google/GitHub sign-in on: the added-method notice still goes out.
		"identity_linked": {features.NewStatic(features.AuthOAuth), oauthOnlyProject()},
	}
	for template, tc := range cases {
		sender := &recordingEmailSender{}
		w := postAccountMail(accountMailRouter(sender, tc.flags, tc.source), template)
		if w.Code != http.StatusAccepted {
			t.Fatalf("%s: want 202, got %d body=%s", template, w.Code, w.Body.String())
		}
		msg, ok := sender.last()
		if !ok || len(msg.To) != 1 || msg.To[0] != testRecipient || msg.Tags["template"] != template {
			t.Fatalf("%s: message = %+v", template, msg)
		}
		if !strings.Contains(msg.Subject, "Acme Notes") || msg.FromName != "Acme Notes via Excalibase" {
			t.Errorf("%s: the mail names no project: %q from %q", template, msg.Subject, msg.FromName)
		}
		if tc.source.taken != 1 {
			t.Errorf("%s: the send spent %d of the budget", template, tc.source.taken)
		}
	}
}

func TestInternalEmailSend_EmailChangeMailNeedsTheUserFlagAndAProject(t *testing.T) {
	ghost := signInMailOn()
	ghost.project = domain.SignInMailProject{}
	spent := signInMailOn()
	spent.left = 0
	everySignIn := features.NewStatic(features.AuthOAuth, features.AuthOTP)
	cases := map[string]struct {
		flags  features.Flags
		source *fakeSignInMail
		want   int
	}{
		"the flag off":              {everySignIn, signInMailOn(), http.StatusForbidden},
		"no settings wired":         {features.NewStatic(features.AuthUser), nil, http.StatusForbidden},
		"an unknown project":        {features.NewStatic(features.AuthUser), ghost, http.StatusForbidden},
		"settings unreadable":       {features.NewStatic(features.AuthUser), &fakeSignInMail{readErr: errors.New("db down")}, http.StatusServiceUnavailable},
		"the day's budget spent":    {features.NewStatic(features.AuthUser), spent, http.StatusTooManyRequests},
		"the budget cannot be read": {features.NewStatic(features.AuthUser), &fakeSignInMail{project: signInMailOn().project, takeErr: errors.New("db down")}, http.StatusServiceUnavailable},
	}
	for name, tc := range cases {
		for _, template := range []string{"email_change", "email_change_notice"} {
			sender := &recordingEmailSender{}
			if w := postAccountMail(accountMailRouter(sender, tc.flags, tc.source), template); w.Code != tc.want || len(sender.sent) != 0 {
				t.Errorf("%s, %s: %d and %d sent, want %d", name, template, w.Code, len(sender.sent), tc.want)
			}
		}
	}
}

func TestInternalEmailSend_IdentityLinkedFollowsTheProjectsSignInMethods(t *testing.T) {
	otpOnly := signInMailOn()
	noProvider := signInMailOn()
	noProvider.project.EmailOTPEnabled = false
	spent := oauthOnlyProject()
	spent.left = 0
	cases := map[string]struct {
		flags  features.Flags
		source *fakeSignInMail
		want   int
	}{
		"oauth on with a provider":         {features.NewStatic(features.AuthOAuth), oauthOnlyProject(), http.StatusAccepted},
		"email sign-in on with its switch": {features.NewStatic(features.AuthOTP), otpOnly, http.StatusAccepted},
		"oauth on without a provider":      {features.NewStatic(features.AuthOAuth), otpOnly, http.StatusForbidden},
		"email sign-in on, its switch off": {features.NewStatic(features.AuthOTP), oauthOnlyProject(), http.StatusForbidden},
		"both on, neither method in use":   {features.NewStatic(features.AuthOAuth, features.AuthOTP), noProvider, http.StatusForbidden},
		"only the user flag":               {features.NewStatic(features.AuthUser), oauthOnlyProject(), http.StatusForbidden},
		"no settings wired":                {features.NewStatic(features.AuthOAuth), nil, http.StatusForbidden},
		"settings unreadable":              {features.NewStatic(features.AuthOAuth), &fakeSignInMail{readErr: errors.New("db down")}, http.StatusServiceUnavailable},
		"the day's budget spent":           {features.NewStatic(features.AuthOAuth), spent, http.StatusTooManyRequests},
		"the budget cannot be read":        {features.NewStatic(features.AuthOAuth), &fakeSignInMail{project: oauthOnlyProject().project, takeErr: errors.New("db down")}, http.StatusServiceUnavailable},
	}
	for name, tc := range cases {
		sender := &recordingEmailSender{}
		w := postAccountMail(accountMailRouter(sender, tc.flags, tc.source), "identity_linked")
		if sent := len(sender.sent); w.Code != tc.want || (sent == 1) != (tc.want == http.StatusAccepted) {
			t.Errorf("%s: %d and %d sent, want %d", name, w.Code, sent, tc.want)
		}
	}
}

func TestInternalEmailSend_AccountMailRefusesBadData(t *testing.T) {
	bad := map[string]string{
		"email_change":        `{"userEmail":"` + testNewAddress + `","linkUrl":"javascript:alert(1)"}`,
		"email_change_notice": `{"userEmail":"` + testRecipient + `","newEmail":"x\r\nBcc: y@example.com"}`,
		"identity_linked":     `{"userEmail":"` + testRecipient + `","method":"facebook"}`,
	}
	flags := features.NewStatic(features.AuthUser, features.AuthOAuth)
	for template, data := range bad {
		sender := &recordingEmailSender{}
		source := oauthOnlyProject()
		w := postInternalEmail(accountMailRouter(sender, flags, source), `{"projectId":"proj_p1","to":"`+testRecipient+`","template":"`+template+`","data":`+data+`}`)
		if w.Code != http.StatusBadRequest || len(sender.sent) != 0 || source.taken != 0 {
			t.Errorf("%s: %d, %d sent, %d budget spent", template, w.Code, len(sender.sent), source.taken)
		}
	}
}

func TestInternalEmailSend_AccountMailLogsNoAddresses(t *testing.T) {
	spent := signInMailOn()
	spent.left = 0
	logged := captureLogs(t, func() {
		for template := range accountMailBodies {
			postAccountMail(accountMailRouter(&recordingEmailSender{}, features.NewStatic(features.AuthUser, features.AuthOTP), spent), template)
		}
	})
	if !strings.Contains(logged, "budget spent") {
		t.Fatalf("nothing logged: %q", logged)
	}
	for _, secret := range []string{testRecipient, testNewAddress, "token_hash"} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log carries %q: %q", secret, logged)
		}
	}
}

// A project's end users changing addresses must not spend its sign-in mail.
func TestInternalEmailSend_AccountMailHasItsOwnBudget(t *testing.T) {
	flags := features.NewStatic(features.AuthUser, features.AuthOAuth, features.AuthOTP)
	source := signInMailOn()
	source.project.OAuthEnabled = true
	source.spent = map[string]bool{domain.MailBudgetSignIn: true}
	for template := range accountMailBodies {
		if w := postAccountMail(accountMailRouter(&recordingEmailSender{}, flags, source), template); w.Code != http.StatusAccepted {
			t.Errorf("%s with sign-in mail spent: %d %s", template, w.Code, w.Body.String())
		}
	}
	if w := postSignInMail(accountMailRouter(&recordingEmailSender{}, flags, source), "magic_link"); w.Code != http.StatusTooManyRequests {
		t.Errorf("sign-in mail with its budget spent: %d", w.Code)
	}
	want := []string{domain.MailBudgetAccount, domain.MailBudgetAccount, domain.MailBudgetAccount, domain.MailBudgetSignIn}
	if !slices.Equal(source.kinds, want) {
		t.Fatalf("budgets drawn on: %v", source.kinds)
	}

	source = signInMailOn()
	source.project.OAuthEnabled = true
	source.spent = map[string]bool{domain.MailBudgetAccount: true}
	for template := range accountMailBodies {
		if w := postAccountMail(accountMailRouter(&recordingEmailSender{}, flags, source), template); w.Code != http.StatusTooManyRequests {
			t.Errorf("%s with account mail spent: %d", template, w.Code)
		}
	}
	if w := postSignInMail(accountMailRouter(&recordingEmailSender{}, flags, source), "email_otp"); w.Code != http.StatusAccepted {
		t.Errorf("sign-in mail with account mail spent: %d", w.Code)
	}
}
