//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
type capturingSender struct {
	last email.Message
}

func (c *capturingSender) Send(_ context.Context, m email.Message) (string, error) {
	c.last = m
	return "msg-test-1", nil
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

	user := &domain.User{ID: "user-verify", Username: testutil.FixturePassword("vuser"), Email: "v@example.com"}

	// SendVerify (authenticated).
	req := httptest.NewRequest("POST", "/verify/send", nil)
	req = req.WithContext(auth.SetUser(req.Context(), user))
	w := httptest.NewRecorder()
	h.SendVerify(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SendVerify: %d body=%s", w.Code, w.Body.String())
	}
	token := tokenFromURL(sender.last.HTMLBody)
	if token == "" {
		t.Fatalf("no token in verify email body: %s", sender.last.HTMLBody)
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
	token := tokenFromURL(sender.last.HTMLBody)
	if token == "" {
		t.Fatalf("no token in reset email body: %s", sender.last.HTMLBody)
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
