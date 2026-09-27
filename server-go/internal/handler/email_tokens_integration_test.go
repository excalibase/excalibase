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
	"time"

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
	h.SetSessionStore(store)

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

func seedToken(t *testing.T, store interface {
	CreateToken(context.Context, *domain.AccessToken) error
}, userID, hash, scopes string) {
	t.Helper()
	now := time.Now()
	later := now.Add(time.Hour)
	if err := store.CreateToken(context.Background(), &domain.AccessToken{
		TokenHash: hash, TokenPrefix: hash[:8], UserID: userID, Name: scopes,
		Scopes: scopes, CreatedAt: &now, ExpiresAt: &later,
	}); err != nil {
		t.Fatalf("seed token: %v", err)
	}
}

// A reset is the remedy for a stolen account: every session and every access
// token that existed before it, the attacker's included, must stop working.
func TestEmailTokens_ResetEndsEverySessionAndRevokesEveryAccessToken(t *testing.T) {
	store := pgtest.New(t)
	sender := &capturingSender{}
	h := NewEmailTokensHandler(store.DB(), sender, store, "https://app.example.com", "Excalibase")
	h.SetVerifier(NewEmailVerifier(store, sender, "https://app.example.com", "Excalibase"))
	h.SetSessionStore(store)
	h.runInBackground = func(f func()) { f() }

	victim := &domain.User{ID: "user-victim", Username: testutil.FixturePassword("victim"), Email: "victim@example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}
	other := &domain.User{ID: "user-other", Username: testutil.FixturePassword("other"), Email: "other@example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}
	for _, u := range []*domain.User{victim, other} {
		if err := store.CreateUser(context.Background(), u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}
	seedToken(t, store, victim.ID, "hash-victim-session-a", "session")
	seedToken(t, store, victim.ID, "hash-victim-session-b", "session")
	seedToken(t, store, victim.ID, "hash-victim-ci-token", "read,write")
	seedToken(t, store, victim.ID, "hash-victim-legacy-token", "")
	seedToken(t, store, other.ID, "hash-other-session-1", "session")
	seedToken(t, store, other.ID, "hash-other-ci-token", "read")

	h.SendReset(httptest.NewRecorder(), httptest.NewRequest("POST", "/reset/send", strings.NewReader(`{"email":"victim@example.com"}`)))
	token := tokenFromURL(sender.message().HTMLBody)
	w := httptest.NewRecorder()
	h.ConfirmReset(w, httptest.NewRequest("POST", "/reset/confirm",
		strings.NewReader(`{"token":"`+token+`","newPassword":"brand-new-pass"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("ConfirmReset: %d %s", w.Code, w.Body.String())
	}
	var answer struct {
		Status              string `json:"status"`
		AccessTokensRevoked int    `json:"accessTokensRevoked"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if answer.Status != "reset" || answer.AccessTokensRevoked != 2 {
		t.Errorf("answer = %+v, want status reset and 2 access tokens revoked", answer)
	}

	for _, hash := range []string{"hash-victim-session-a", "hash-victim-session-b", "hash-victim-ci-token", "hash-victim-legacy-token"} {
		if tok, _ := store.FindByTokenHash(context.Background(), hash); tok != nil {
			t.Errorf("token %s survived the reset", hash)
		}
	}
	for _, hash := range []string{"hash-other-session-1", "hash-other-ci-token"} {
		if tok, _ := store.FindByTokenHash(context.Background(), hash); tok == nil {
			t.Errorf("another account's token %s was revoked", hash)
		}
	}
}

// Without a place to revoke sessions the reset must refuse, not succeed while
// leaving them alive.
func TestEmailTokens_ResetRefusesWithoutASessionStore(t *testing.T) {
	store := pgtest.New(t)
	sender := &capturingSender{}
	h := NewEmailTokensHandler(store.DB(), sender, store, "https://app.example.com", "Excalibase")
	h.runInBackground = func(f func()) { f() }
	user := &domain.User{ID: "user-nostore", Username: testutil.FixturePassword("nostore"), Email: "n@example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	h.SendReset(httptest.NewRecorder(), httptest.NewRequest("POST", "/reset/send", strings.NewReader(`{"email":"n@example.com"}`)))
	w := httptest.NewRecorder()
	h.ConfirmReset(w, httptest.NewRequest("POST", "/reset/confirm",
		strings.NewReader(`{"token":"`+tokenFromURL(sender.message().HTMLBody)+`","newPassword":"brand-new-pass"}`)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", w.Code)
	}
	if updated, _ := store.FindUserByID(context.Background(), user.ID); updated.PasswordHash != user.PasswordHash {
		t.Error("the password changed although sessions could not be ended")
	}
}

// failingSessions stands in for a token store that errors on list or delete.
type failingSessions struct {
	sessionTokens
	failList bool
}

func (f failingSessions) ListTokensByUser(ctx context.Context, userID string) ([]*domain.AccessToken, error) {
	if f.failList {
		return nil, errors.New("list failed")
	}
	return f.sessionTokens.ListTokensByUser(ctx, userID)
}

func (f failingSessions) DeleteToken(context.Context, string) error {
	return errors.New("delete failed")
}

// When sessions cannot be ended the reset must not report success, or the
// owner believes a stolen session is gone while it still works.
func TestEmailTokens_ResetReportsSessionsThatCouldNotBeEnded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failList bool
	}{{"list fails", true}, {"delete fails", false}} {
		t.Run(tc.name, func(t *testing.T) {
			store := pgtest.New(t)
			sender := &capturingSender{}
			h := NewEmailTokensHandler(store.DB(), sender, store, "https://app.example.com", "Excalibase")
			h.SetVerifier(NewEmailVerifier(store, sender, "https://app.example.com", "Excalibase"))
			h.SetSessionStore(failingSessions{sessionTokens: store, failList: tc.failList})
			h.runInBackground = func(f func()) { f() }
			user := &domain.User{ID: "user-stuck", Username: testutil.FixturePassword("stuck"), Email: "s@example.com",
				PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}
			if err := store.CreateUser(context.Background(), user); err != nil {
				t.Fatalf("CreateUser: %v", err)
			}
			seedToken(t, store, user.ID, "hash-stuck-session", "session")

			h.SendReset(httptest.NewRecorder(), httptest.NewRequest("POST", "/reset/send", strings.NewReader(`{"email":"s@example.com"}`)))
			w := httptest.NewRecorder()
			h.ConfirmReset(w, httptest.NewRequest("POST", "/reset/confirm",
				strings.NewReader(`{"token":"`+tokenFromURL(sender.message().HTMLBody)+`","newPassword":"brand-new-pass"}`)))
			if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "could not be signed out") {
				t.Fatalf("got %d %s, want 500 naming the sessions", w.Code, w.Body.String())
			}
		})
	}
}

// recordingTokens deletes through the real store, remembers the order, and
// fails on the hash named in failOn.
type recordingTokens struct {
	sessionTokens
	failOn  string
	deleted *[]string
}

func (r recordingTokens) DeleteToken(ctx context.Context, hash string) error {
	if hash == r.failOn {
		return errors.New("delete failed")
	}
	*r.deleted = append(*r.deleted, hash)
	return r.sessionTokens.DeleteToken(ctx, hash)
}

func resetWithTokens(t *testing.T, failOn string) (*httptest.ResponseRecorder, []string) {
	t.Helper()
	store := pgtest.New(t)
	sender := &capturingSender{}
	h := NewEmailTokensHandler(store.DB(), sender, store, "https://app.example.com", "Excalibase")
	h.SetVerifier(NewEmailVerifier(store, sender, "https://app.example.com", "Excalibase"))
	var deleted []string
	h.SetSessionStore(recordingTokens{sessionTokens: store, failOn: failOn, deleted: &deleted})
	h.runInBackground = func(f func()) { f() }
	user := &domain.User{ID: "user-order", Username: testutil.FixturePassword("order"), Email: "o@example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	seedToken(t, store, user.ID, "hash-order-ci-token", "read")
	seedToken(t, store, user.ID, "hash-order-session", "session")

	h.SendReset(httptest.NewRecorder(), httptest.NewRequest("POST", "/reset/send", strings.NewReader(`{"email":"o@example.com"}`)))
	w := httptest.NewRecorder()
	h.ConfirmReset(w, httptest.NewRequest("POST", "/reset/confirm",
		strings.NewReader(`{"token":"`+tokenFromURL(sender.message().HTMLBody)+`","newPassword":"brand-new-pass"}`)))
	return w, deleted
}

// Sessions end before access tokens are revoked, so a token failure never
// leaves a live session behind.
func TestEmailTokens_ResetEndsSessionsBeforeRevokingAccessTokens(t *testing.T) {
	w, deleted := resetWithTokens(t, "")
	if w.Code != http.StatusOK {
		t.Fatalf("ConfirmReset: %d %s", w.Code, w.Body.String())
	}
	if want := []string{"hash-order-session", "hash-order-ci-token"}; strings.Join(deleted, ",") != strings.Join(want, ",") {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
}

// When an access token cannot be revoked the reset must not report success,
// or the owner believes a stolen script token is dead while it still works.
func TestEmailTokens_ResetReportsAccessTokensThatCouldNotBeRevoked(t *testing.T) {
	w, _ := resetWithTokens(t, "hash-order-ci-token")
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "access tokens could not be revoked") {
		t.Fatalf("got %d %s, want 500 naming the access tokens", w.Code, w.Body.String())
	}
}
