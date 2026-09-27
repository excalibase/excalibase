package handler

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
)

type emailFlowHarness struct {
	router chi.Router
	users  *mockUserStore
	links  *fakeVerificationStore
	sender *recordingSender
}

func newEmailFlowHarness(sender email.Sender) *emailFlowHarness {
	h := &emailFlowHarness{users: newMockUserStore(), links: newFakeVerificationStore()}
	h.sender, _ = sender.(*recordingSender)
	tokens := NewEmailTokensHandler(nil, sender, h.users, testStudioURL, "")
	tokens.SetVerifier(NewEmailVerifier(h.links, sender, testStudioURL, ""))
	tokens.runInBackground = func(f func()) { f() }
	r := chi.NewRouter()
	tokens.Routes(r)
	h.router = r
	return h
}

func (h *emailFlowHarness) seed(id, address string, verified bool) {
	user := &domain.User{ID: id, Username: id, Email: address, Active: true, Kind: domain.UserKindHuman}
	if verified {
		now := time.Now()
		user.EmailVerifiedAt = &now
	}
	h.users.users[id] = user
	h.links.users[id] = address
}

func TestResendMailsAnUnverifiedAccountANewLink(t *testing.T) {
	h := newEmailFlowHarness(&recordingSender{})
	h.seed("u1", "dev@example.com", false)

	if w := doRequest(h.router, "POST", "/verify/resend", `{"email":"dev@example.com"}`); w.Code != http.StatusOK {
		t.Fatalf("resend: %d %s", w.Code, w.Body.String())
	}
	if len(h.sender.sent) != 1 || h.sender.sent[0].To[0] != "dev@example.com" {
		t.Fatalf("sent: %+v", h.sender.sent)
	}
	w := doRequest(h.router, "POST", "/verify/confirm", `{"token":"`+h.sender.lastToken(t)+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	if _, ok := h.links.verified["u1"]; !ok {
		t.Fatal("confirming the resent link did not verify the account")
	}
}

// The answer is the same whether or not the address has an account, so the
// route cannot be used to discover who is registered.
func TestResendAnswersTheSameForEveryAddress(t *testing.T) {
	h := newEmailFlowHarness(&recordingSender{})
	h.seed("u1", "done@example.com", true)
	for _, address := range []string{"done@example.com", "nobody@example.com"} {
		w := doRequest(h.router, "POST", "/verify/resend", `{"email":"`+address+`"}`)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"sent"`) {
			t.Errorf("%s: %d %s", address, w.Code, w.Body.String())
		}
	}
	if len(h.sender.sent) != 0 {
		t.Fatalf("mail went to a verified or unknown address: %+v", h.sender.sent)
	}
}

func TestResendRequiresAnAddressAndAMailer(t *testing.T) {
	h := newEmailFlowHarness(&recordingSender{})
	if w := doRequest(h.router, "POST", "/verify/resend", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("no address: %d", w.Code)
	}
	noMail := newEmailFlowHarness(email.NewNoopSender())
	if w := doRequest(noMail.router, "POST", "/verify/resend", `{"email":"a@example.com"}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("no mailer: %d", w.Code)
	}
}

func TestConfirmRefusesAnUnknownLink(t *testing.T) {
	h := newEmailFlowHarness(&recordingSender{})
	if w := doRequest(h.router, "POST", "/verify/confirm", `{"token":"nope"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestResendIsBehindThePublicLimit(t *testing.T) {
	tokens := NewEmailTokensHandler(nil, &recordingSender{}, newMockUserStore(), testStudioURL, "")
	tokens.SetPublicLimit(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	})
	r := chi.NewRouter()
	tokens.Routes(r)
	if w := doRequest(r, "POST", "/verify/resend", `{"email":"a@example.com"}`); w.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d, want the limiter's 429", w.Code)
	}
}

func TestVerifyRoutesWithoutAVerifierAre503(t *testing.T) {
	tokens := NewEmailTokensHandler(nil, &recordingSender{}, newMockUserStore(), testStudioURL, "")
	r := chi.NewRouter()
	tokens.Routes(r)
	if w := doRequest(r, "POST", "/verify/confirm", `{"token":"t"}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("confirm: %d", w.Code)
	}
}

func TestSendVerifyReportsAFailedSend(t *testing.T) {
	sender := &recordingSender{err: errNotSent}
	tokens := NewEmailTokensHandler(nil, sender, newMockUserStore(), testStudioURL, "")
	tokens.SetVerifier(NewEmailVerifier(newFakeVerificationStore(), sender, testStudioURL, ""))
	w := httptestPostAs(tokens.SendVerify, &domain.User{ID: "u1", Email: "dev@example.com"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", w.Code)
	}
	noMail := NewEmailTokensHandler(nil, nil, newMockUserStore(), testStudioURL, "")
	if w := httptestPostAs(noMail.SendVerify, &domain.User{ID: "u1", Email: "dev@example.com"}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no verifier: got %d, want 503", w.Code)
	}
}

func TestResendKeepsItsAnswerWhenTheMailFails(t *testing.T) {
	h := newEmailFlowHarness(&recordingSender{err: errNotSent})
	h.seed("u1", "dev@example.com", false)
	if w := doRequest(h.router, "POST", "/verify/resend", `{"email":"dev@example.com"}`); w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
}
