//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

// capturingSender records the last message so tests can pull the click token
// out of the verify/reset URL (the raw token is never returned in the API
// response — it only travels by email).
// Concurrent registrations send through one sender, so access is locked.
type capturingSender struct {
	mu     sync.Mutex
	latest email.Message
}

func (c *capturingSender) Send(_ context.Context, m email.Message) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latest = m
	return "msg-test-1", nil
}

func (c *capturingSender) message() email.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest
}

// tokenFromURL extracts the ?token=... value from the most recent message body.
func tokenFromURL(body string) string {
	i := strings.Index(body, "token=")
	if i < 0 {
		return ""
	}
	rest := body[i+len("token="):]
	// Token ends at the next quote, whitespace, or angle bracket.
	for j := 0; j < len(rest); j++ {
		c := rest[j]
		if c == '"' || c == '\'' || c == ' ' || c == '<' || c == '\n' || c == '&' {
			return rest[:j]
		}
	}
	return rest
}

func TestEmailTokens_VerifyFlow(t *testing.T) {
	store := pgtest.New(t)
	sender := &capturingSender{}
	h := NewEmailTokensHandler(store.DB(), sender, store, "https://app.example.com", "Excalibase")
	h.SetVerifier(NewEmailVerifier(store, sender, "https://app.example.com", "Excalibase"))
	h.runInBackground = func(f func()) { f() }

	user := &domain.User{ID: "user-verify", Username: testutil.FixturePassword("vuser"), Email: "v@example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// SendVerify (authenticated).
	req := httptest.NewRequest("POST", "/verify/send", nil)
	req = req.WithContext(auth.SetUser(req.Context(), user))
	w := httptest.NewRecorder()
	h.SendVerify(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendVerify: %d body=%s", w.Code, w.Body.String())
	}
	token := tokenFromURL(sender.message().HTMLBody)
	if token == "" {
		t.Fatalf("no token in verify email body: %s", sender.message().HTMLBody)
	}

	// ConfirmVerify with the captured token.
	body := `{"token":"` + token + `"}`
	req = httptest.NewRequest("POST", "/verify/confirm", strings.NewReader(body))
	w = httptest.NewRecorder()
	h.ConfirmVerify(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ConfirmVerify: %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "verified" || resp["userId"] != user.ID {
		t.Errorf("unexpected confirm response: %+v", resp)
	}
	if stored, _ := store.FindUserByID(context.Background(), user.ID); stored == nil || stored.EmailVerifiedAt == nil {
		t.Error("confirming the link did not verify the account")
	}

	// Re-confirming the same token is rejected (consumed).
	req = httptest.NewRequest("POST", "/verify/confirm", strings.NewReader(body))
	w = httptest.NewRecorder()
	h.ConfirmVerify(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("second confirm should 400 (consumed), got %d", w.Code)
	}
}

func TestEmailTokens_ResetFlow(t *testing.T) {
	store := pgtest.New(t)
	sender := &capturingSender{}
	h := NewEmailTokensHandler(store.DB(), sender, store, "https://app.example.com", "Excalibase")
	h.SetVerifier(NewEmailVerifier(store, sender, "https://app.example.com", "Excalibase"))
	h.runInBackground = func(f func()) { f() }

	// Seed a user with a known password.
	origHash, _ := auth.HashPassword(testutil.FixturePassword("old"))
	user := &domain.User{ID: "user-reset", Username: testutil.FixturePassword("ruser"), Email: "r@example.com", PasswordHash: origHash, Role: "user", Active: true}
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// SendReset for the known email.
	req := httptest.NewRequest("POST", "/reset/send", strings.NewReader(`{"email":"r@example.com"}`))
	w := httptest.NewRecorder()
	h.SendReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendReset: %d body=%s", w.Code, w.Body.String())
	}
	token := tokenFromURL(sender.message().HTMLBody)
	if token == "" {
		t.Fatalf("no token in reset email body: %s", sender.message().HTMLBody)
	}
	if !strings.Contains(sender.message().TextBody, "https://app.example.com/reset-password?token=") {
		t.Fatalf("reset link is not a Studio link: %s", sender.message().TextBody)
	}

	// ConfirmReset with a new password.
	req = httptest.NewRequest("POST", "/reset/confirm",
		strings.NewReader(`{"token":"`+token+`","newPassword":"brand-new-pass"}`))
	w = httptest.NewRecorder()
	h.ConfirmReset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ConfirmReset: %d body=%s", w.Code, w.Body.String())
	}

	// The stored password hash should now verify the new password.
	updated, _ := store.FindUserByID(context.Background(), user.ID)
	if updated == nil || !auth.CheckPassword("brand-new-pass", updated.PasswordHash) {
		t.Errorf("password not updated to new value")
	}
	// The link reached the account's mailbox, which proves the address.
	if updated == nil || updated.EmailVerifiedAt == nil {
		t.Errorf("a completed reset did not verify the address")
	}
}

func TestEmailTokens_SendReset_UnknownEmailStill200(t *testing.T) {
	store := pgtest.New(t)
	h := NewEmailTokensHandler(store.DB(), email.NewNoopSender(), store, "https://app", "App")
	req := httptest.NewRequest("POST", "/reset/send", strings.NewReader(`{"email":"nobody@example.com"}`))
	w := httptest.NewRecorder()
	h.SendReset(w, req)
	// Always 200 to avoid disclosing whether an email is registered.
	if w.Code != http.StatusOK {
		t.Errorf("unknown email should still 200, got %d", w.Code)
	}
}

func TestEmailTokens_Routes_Mountable(t *testing.T) {
	store := pgtest.New(t)
	h := NewEmailTokensHandler(store.DB(), email.NewNoopSender(), store, "https://app", "App")
	r := chi.NewRouter()
	h.Routes(r) // exercises Routes wiring
}

// Two confirms that both passed the lookup before either consumed the link:
// only the first claim may win, or one emailed link sets two passwords.
func TestEmailTokens_ResetLinkIsClaimedOnce(t *testing.T) {
	store := pgtest.New(t)
	sender := &capturingSender{}
	h := NewEmailTokensHandler(store.DB(), sender, store, "https://app.example.com", "Excalibase")
	h.runInBackground = func(f func()) { f() }
	user := &domain.User{ID: "user-claim", Username: testutil.FixturePassword("cuser"), Email: "c@example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	h.SendReset(httptest.NewRecorder(), httptest.NewRequest("POST", "/reset/send", strings.NewReader(`{"email":"c@example.com"}`)))
	hash := hashToken(tokenFromURL(sender.message().HTMLBody))

	ok, err := h.claimReset(context.Background(), hash)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	if ok, err := h.claimReset(context.Background(), hash); ok || err != nil {
		t.Fatalf("second claim: ok=%v err=%v, want a plain refusal", ok, err)
	}
}

// Registered or not, the caller sees the same answer; only a registered
// address is mailed.
func TestEmailTokens_ResetSendAnswersAlikeForEveryAddress(t *testing.T) {
	store := pgtest.New(t)
	sender := &recordingSender{}
	h := NewEmailTokensHandler(store.DB(), sender, store, "https://app.example.com", "Excalibase")
	h.runInBackground = func(f func()) { f() }
	user := &domain.User{ID: "user-alike", Username: testutil.FixturePassword("auser"), Email: "a@example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	send := func(address string) (int, string) {
		w := httptest.NewRecorder()
		h.SendReset(w, httptest.NewRequest("POST", "/reset/send", strings.NewReader(`{"email":"`+address+`"}`)))
		return w.Code, w.Body.String()
	}
	knownCode, knownBody := send("a@example.com")
	unknownCode, unknownBody := send("nobody@example.com")
	if knownCode != unknownCode || knownBody != unknownBody {
		t.Fatalf("answers differ: %d %q vs %d %q", knownCode, knownBody, unknownCode, unknownBody)
	}
	if len(sender.sent) != 1 || sender.sent[0].To[0] != "a@example.com" {
		t.Fatalf("mail sent: %+v", sender.sent)
	}
}

// A provider failure for a registered address must not surface as a
// different answer than an unregistered one gets.
func TestEmailTokens_ResetSendHidesAProviderFailure(t *testing.T) {
	store := pgtest.New(t)
	sender := &recordingSender{err: errors.New("provider down")}
	h := NewEmailTokensHandler(store.DB(), sender, store, "https://app.example.com", "Excalibase")
	h.runInBackground = func(f func()) { f() }
	user := &domain.User{ID: "user-down", Username: testutil.FixturePassword("duser"), Email: "d@example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	w := httptest.NewRecorder()
	h.SendReset(w, httptest.NewRequest("POST", "/reset/send", strings.NewReader(`{"email":"d@example.com"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: a failed send for a registered address answers differently", w.Code)
	}
}
