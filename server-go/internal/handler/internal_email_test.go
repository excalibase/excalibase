package handler

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/go-chi/chi/v5"
)

const (
	internalEmailPath = "/internal/email/send"
	testRecipient     = "someone@example.com"
	testVerifyURL     = "https://app.example.io/verify?token=abc123"
	testResetURL      = "https://app.example.io/reset?token=def456"
)

// recordingEmailSender captures every Message handed to it so tests can assert
// on the rendered template without touching a real provider.
type recordingEmailSender struct {
	mu   sync.Mutex
	sent []email.Message
	err  error
}

func (s *recordingEmailSender) Send(_ context.Context, msg email.Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	s.sent = append(s.sent, msg)
	return "msg-1", nil
}

func (s *recordingEmailSender) last() (email.Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return email.Message{}, false
	}
	return s.sent[len(s.sent)-1], true
}

// newInternalEmailRouter mounts the bare handler. Authorization lives in the
// middleware chain the production router wraps it in (see the relay authz
// tests in cmd/server), so these tests cover rendering and dispatch only.
func newInternalEmailRouter(sender email.Sender) *chi.Mux {
	r := chi.NewRouter()
	NewInternalEmailHandler(sender).Routes(r)
	return r
}

func postInternalEmail(r *chi.Mux, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", internalEmailPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestInternalEmailSend_VerifyEmailAccepted(t *testing.T) {
	sender := &recordingEmailSender{}
	r := newInternalEmailRouter(sender)

	body := `{"projectId":"proj_p1","to":"` + testRecipient + `","template":"verify_email",
		"data":{"userEmail":"` + testRecipient + `","verifyUrl":"` + testVerifyURL + `","expiresHour":"12"}}`
	w := postInternalEmail(r, body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d body=%s", w.Code, w.Body.String())
	}
	msg, ok := sender.last()
	if !ok {
		t.Fatal("sender received no message")
	}
	if len(msg.To) != 1 || msg.To[0] != testRecipient {
		t.Errorf("To: got %v", msg.To)
	}
	if msg.Subject == "" || msg.HTMLBody == "" {
		t.Errorf("subject/html body must be non-empty")
	}
	if !strings.Contains(msg.HTMLBody, testVerifyURL) {
		t.Errorf("html body must contain the verify url")
	}
}

func TestInternalEmailSend_PasswordResetAccepted(t *testing.T) {
	sender := &recordingEmailSender{}
	r := newInternalEmailRouter(sender)

	body := `{"projectId":"proj_p1","to":"` + testRecipient + `","template":"password_reset",
		"data":{"userEmail":"` + testRecipient + `","resetUrl":"` + testResetURL + `","expiresMin":"30"}}`
	w := postInternalEmail(r, body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d body=%s", w.Code, w.Body.String())
	}
	msg, ok := sender.last()
	if !ok {
		t.Fatal("sender received no message")
	}
	if len(msg.To) != 1 || msg.To[0] != testRecipient {
		t.Errorf("To: got %v", msg.To)
	}
	if msg.Subject == "" || msg.HTMLBody == "" {
		t.Errorf("subject/html body must be non-empty")
	}
	if !strings.Contains(msg.HTMLBody, testResetURL) {
		t.Errorf("html body must contain the reset url")
	}
}

// fakeSignInMail is the project a sign-in mail is for and its daily budget.
type fakeSignInMail struct {
	project domain.SignInMailProject
	readErr error
	left    int
	takeErr error
	taken   int
}

func (f *fakeSignInMail) SignInMailProject(context.Context, string) (domain.SignInMailProject, error) {
	return f.project, f.readErr
}

func (f *fakeSignInMail) TakeSignInMail(_ context.Context, _ string, perDay int) (bool, error) {
	if f.takeErr != nil {
		return false, f.takeErr
	}
	if perDay <= 0 || f.left == 0 {
		return false, nil
	}
	f.left--
	f.taken++
	return true, nil
}

func signInMailOn() *fakeSignInMail {
	return &fakeSignInMail{project: domain.SignInMailProject{Name: "Acme Notes", SiteURL: "https://notes.acme.test", EmailOTPEnabled: true}, left: 100}
}

func newSignInMailRouter(sender email.Sender, flags features.Flags, source *fakeSignInMail) *chi.Mux {
	r := chi.NewRouter()
	NewInternalEmailHandler(sender).WithSignInMail(flags, source).Routes(r)
	return r
}

var signInMailBodies = map[string]string{
	"magic_link": `{"userEmail":"` + testRecipient + `","linkUrl":"https://notes.acme.test/cb?token_hash=t1&type=magiclink","expiresMin":"10"}`,
	"email_otp":  `{"userEmail":"` + testRecipient + `","code":"04291735","expiresMin":"10"}`,
	// The address-link mails go to an address being added to an account.
	"link_magic_link": `{"userEmail":"` + testRecipient + `","linkUrl":"https://notes.acme.test/cb?token_hash=t1&type=magiclink","expiresMin":"10"}`,
	"link_email_otp":  `{"userEmail":"` + testRecipient + `","code":"04291735","expiresMin":"10"}`,
}

func postSignInMail(r *chi.Mux, template string) *httptest.ResponseRecorder {
	return postInternalEmail(r, `{"projectId":"proj_p1","to":"`+testRecipient+`","template":"`+template+`","data":`+signInMailBodies[template]+`}`)
}

// EXC-662: the auth service's sign-in link and code, named for the project.
func TestInternalEmailSend_SignInTemplatesAccepted(t *testing.T) {
	for template := range signInMailBodies {
		sender := &recordingEmailSender{}
		source := signInMailOn()
		w := postSignInMail(newSignInMailRouter(sender, features.NewStatic(features.AuthOTP), source), template)
		if w.Code != http.StatusAccepted {
			t.Fatalf("%s: want 202, got %d body=%s", template, w.Code, w.Body.String())
		}
		msg, ok := sender.last()
		if !ok || len(msg.To) != 1 || msg.To[0] != testRecipient || msg.Tags["template"] != template {
			t.Fatalf("%s: message = %+v", template, msg)
		}
		if !strings.Contains(msg.TextBody, "04291735") && !strings.Contains(msg.TextBody, "token_hash=t1") {
			t.Errorf("%s: the secret is not in the mail", template)
		}
		if !strings.Contains(msg.Subject, "Acme Notes") || !strings.Contains(msg.TextBody, "notes.acme.test") {
			t.Errorf("%s: the mail names no project: %q", template, msg.Subject)
		}
		if source.taken != 1 {
			t.Errorf("%s: the send spent %d of the budget", template, source.taken)
		}
	}
}

func TestInternalEmailSend_SignInMailRefusedUnlessTheProjectMayMail(t *testing.T) {
	off := signInMailOn()
	off.project.EmailOTPEnabled = false
	spent := signInMailOn()
	spent.left = 0
	cases := map[string]struct {
		flags  features.Flags
		source *fakeSignInMail
		want   int
	}{
		"the flag off":              {features.NewStatic(), signInMailOn(), http.StatusForbidden},
		"no settings wired":         {features.NewStatic(features.AuthOTP), nil, http.StatusForbidden},
		"the project's switch off":  {features.NewStatic(features.AuthOTP), off, http.StatusForbidden},
		"settings unreadable":       {features.NewStatic(features.AuthOTP), &fakeSignInMail{readErr: errors.New("db down")}, http.StatusServiceUnavailable},
		"the day's budget spent":    {features.NewStatic(features.AuthOTP), spent, http.StatusTooManyRequests},
		"the budget cannot be read": {features.NewStatic(features.AuthOTP), &fakeSignInMail{project: signInMailOn().project, takeErr: errors.New("db down")}, http.StatusServiceUnavailable},
	}
	for name, tc := range cases {
		for template := range signInMailBodies {
			sender := &recordingEmailSender{}
			r := chi.NewRouter()
			handler := NewInternalEmailHandler(sender)
			if tc.source != nil {
				handler = handler.WithSignInMail(tc.flags, tc.source)
			}
			handler.Routes(r)
			if w := postSignInMail(r, template); w.Code != tc.want || len(sender.sent) != 0 {
				t.Errorf("%s, %s: %d and %d sent, want %d", name, template, w.Code, len(sender.sent), tc.want)
			}
		}
	}
	sender := &recordingEmailSender{}
	verify := `{"projectId":"proj_p1","to":"` + testRecipient + `","template":"verify_email","data":{"userEmail":"` + testRecipient + `","verifyUrl":"` + testVerifyURL + `"}}`
	if w := postInternalEmail(newSignInMailRouter(sender, features.NewStatic(), signInMailOn()), verify); w.Code != http.StatusAccepted {
		t.Fatalf("a verification mail with the sign-in flag off: %d", w.Code)
	}
}

func TestInternalEmailSend_BadRequests(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"malformed json", `{"to":`},
		{"missing to", `{"projectId":"p","template":"verify_email"}`},
		{"missing template", `{"projectId":"p","to":"` + testRecipient + `"}`},
		{"unknown template", `{"projectId":"p","to":"` + testRecipient + `","template":"nope"}`},
		{"magic link without a link", `{"projectId":"p","to":"` + testRecipient + `","template":"magic_link","data":{"linkUrl":"javascript:x"}}`},
		{"code that is not eight digits", `{"projectId":"p","to":"` + testRecipient + `","template":"email_otp","data":{"code":"123456"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &recordingEmailSender{}
			r := newInternalEmailRouter(sender)
			w := postInternalEmail(r, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
			}
			if len(sender.sent) != 0 {
				t.Errorf("sender must not be called")
			}
		})
	}
}

func TestInternalEmailSend_SenderErrorIsBadGateway(t *testing.T) {
	sender := &recordingEmailSender{err: errors.New("provider exploded")}
	r := newInternalEmailRouter(sender)

	body := `{"projectId":"p","to":"` + testRecipient + `","template":"verify_email","data":{"verifyUrl":"` + testVerifyURL + `"}}`
	w := postInternalEmail(r, body)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d body=%s", w.Code, w.Body.String())
	}
}

// A dev deployment running EMAIL_PROVIDER=noop must not look like an outage:
// ErrNotConfigured is accepted (and dropped) rather than surfaced as 502.
func TestInternalEmailSend_NoopSenderAccepted(t *testing.T) {
	r := newInternalEmailRouter(email.NewNoopSender())

	body := `{"projectId":"p","to":"` + testRecipient + `","template":"verify_email","data":{"verifyUrl":"` + testVerifyURL + `"}}`
	w := postInternalEmail(r, body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202 for a noop sender, got %d body=%s", w.Code, w.Body.String())
	}
}

// A deployment with no provider at all must fail loud: the auth service has
// to know the mail it queued was never handed to anyone.
func TestInternalEmailSend_NilSenderIsUnavailable(t *testing.T) {
	r := newInternalEmailRouter(nil)

	body := `{"projectId":"p","to":"` + testRecipient + `","template":"verify_email","data":{"verifyUrl":"` + testVerifyURL + `"}}`
	w := postInternalEmail(r, body)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when no sender is wired, got %d body=%s", w.Code, w.Body.String())
	}
}

// captureLogs redirects the standard logger for the duration of fn and returns
// everything written to it.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)
	fn()
	return buf.String()
}

// A caller that smuggles CR/LF into projectId must be rejected at the boundary,
// and nothing it sent may reach the log — forged log lines are the whole point
// of the attack.
func TestInternalEmailSend_ProjectIDWithControlCharactersRejected(t *testing.T) {
	cases := []struct {
		name      string
		projectID string
	}{
		{"line feed", `proj_p1\nERROR: forged relay failure`},
		{"carriage return", `proj_p1\rERROR: forged relay failure`},
		{"missing", ``},
		{"illegal characters", `proj p1;DROP`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &recordingEmailSender{err: errors.New("provider exploded")}
			r := newInternalEmailRouter(sender)
			body := `{"projectId":"` + tc.projectID + `","to":"` + testRecipient +
				`","template":"verify_email","data":{"verifyUrl":"` + testVerifyURL + `"}}`

			var w *httptest.ResponseRecorder
			logged := captureLogs(t, func() { w = postInternalEmail(r, body) })

			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
			}
			if len(sender.sent) != 0 {
				t.Errorf("sender must not be called")
			}
			if strings.Contains(logged, "forged") {
				t.Errorf("injected text reached the log: %q", logged)
			}
		})
	}
}

// Even for a project id that passed validation, the relay failure log must be
// a single line naming the template by its validated constant.
func TestInternalEmailSend_RelayFailureLogsOneLine(t *testing.T) {
	sender := &recordingEmailSender{err: errors.New("provider exploded")}
	r := newInternalEmailRouter(sender)
	body := `{"projectId":"proj_p1","to":"` + testRecipient +
		`","template":"verify_email","data":{"verifyUrl":"` + testVerifyURL + `"}}`

	logged := captureLogs(t, func() { postInternalEmail(r, body) })

	if strings.Count(strings.TrimSpace(logged), "\n") != 0 {
		t.Errorf("relay failure log must be one line, got %q", logged)
	}
	if !strings.Contains(logged, emailTemplateVerify) {
		t.Errorf("log must name the validated template, got %q", logged)
	}
	if strings.Contains(logged, testRecipient) {
		t.Errorf("log must never carry the recipient address, got %q", logged)
	}
}
