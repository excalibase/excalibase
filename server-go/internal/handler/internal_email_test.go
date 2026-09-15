package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/go-chi/chi/v5"
)

const (
	internalEmailPath = "/internal/email/send"
	testEmailPAT      = "pat-service-token"
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

func newInternalEmailRouter(sender email.Sender, pat string) *chi.Mux {
	r := chi.NewRouter()
	NewInternalEmailHandler(sender, pat).Routes(r)
	return r
}

func postInternalEmail(r *chi.Mux, pat, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", internalEmailPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if pat != "" {
		req.Header.Set("Authorization", sharedBearerPrefix+pat)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestInternalEmailSend_VerifyEmailAccepted(t *testing.T) {
	sender := &recordingEmailSender{}
	r := newInternalEmailRouter(sender, testEmailPAT)

	body := `{"projectId":"proj_p1","to":"` + testRecipient + `","template":"verify_email",
		"data":{"userEmail":"` + testRecipient + `","verifyUrl":"` + testVerifyURL + `","expiresHour":"12"}}`
	w := postInternalEmail(r, testEmailPAT, body)

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
	r := newInternalEmailRouter(sender, testEmailPAT)

	body := `{"projectId":"proj_p1","to":"` + testRecipient + `","template":"password_reset",
		"data":{"userEmail":"` + testRecipient + `","resetUrl":"` + testResetURL + `","expiresMin":"30"}}`
	w := postInternalEmail(r, testEmailPAT, body)

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

func TestInternalEmailSend_WrongPATRejected(t *testing.T) {
	sender := &recordingEmailSender{}
	r := newInternalEmailRouter(sender, testEmailPAT)

	w := postInternalEmail(r, "wrong-token", `{"to":"`+testRecipient+`","template":"verify_email"}`)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if len(sender.sent) != 0 {
		t.Errorf("sender must not be called on a bad PAT")
	}
}

func TestInternalEmailSend_MissingAuthorizationRejected(t *testing.T) {
	r := newInternalEmailRouter(&recordingEmailSender{}, testEmailPAT)

	w := postInternalEmail(r, "", `{"to":"`+testRecipient+`","template":"verify_email"}`)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// Fail closed: with no service PAT configured the route must never accept a
// request, whatever token the caller presents.
func TestInternalEmailSend_NoPATConfiguredFailsClosed(t *testing.T) {
	sender := &recordingEmailSender{}
	r := newInternalEmailRouter(sender, "")

	for _, pat := range []string{"", "anything"} {
		w := postInternalEmail(r, pat, `{"to":"`+testRecipient+`","template":"verify_email"}`)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("pat=%q: want 503, got %d", pat, w.Code)
		}
	}
	if len(sender.sent) != 0 {
		t.Errorf("sender must not be called when no PAT is configured")
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &recordingEmailSender{}
			r := newInternalEmailRouter(sender, testEmailPAT)
			w := postInternalEmail(r, testEmailPAT, tc.body)
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
	r := newInternalEmailRouter(sender, testEmailPAT)

	body := `{"projectId":"p","to":"` + testRecipient + `","template":"verify_email","data":{"verifyUrl":"` + testVerifyURL + `"}}`
	w := postInternalEmail(r, testEmailPAT, body)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d body=%s", w.Code, w.Body.String())
	}
}

// A dev deployment running EMAIL_PROVIDER=noop must not look like an outage:
// ErrNotConfigured is accepted (and dropped) rather than surfaced as 502.
func TestInternalEmailSend_NoopSenderAccepted(t *testing.T) {
	r := newInternalEmailRouter(email.NewNoopSender(), testEmailPAT)

	body := `{"projectId":"p","to":"` + testRecipient + `","template":"verify_email","data":{"verifyUrl":"` + testVerifyURL + `"}}`
	w := postInternalEmail(r, testEmailPAT, body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202 for a noop sender, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInternalEmailSend_NilSenderAccepted(t *testing.T) {
	r := newInternalEmailRouter(nil, testEmailPAT)

	body := `{"projectId":"p","to":"` + testRecipient + `","template":"verify_email","data":{"verifyUrl":"` + testVerifyURL + `"}}`
	w := postInternalEmail(r, testEmailPAT, body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202 when no sender is wired, got %d body=%s", w.Code, w.Body.String())
	}
}
