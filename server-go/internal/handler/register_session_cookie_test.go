package handler

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

func sessionCookieOf(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	return nil
}

// Studio signs in with the cookie alone (EXC-481), so the first admin is only
// signed in after /setup if registration sets the same cookie login does.
func TestRegister_FirstAdmin_SetsTheLoginSessionCookie(t *testing.T) {
	h, raw := firstAdminHandler(t)
	w := postRegisterWithToken(h, "founder", "founder@x.test", raw)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}

	session := sessionCookieOf(t, w.Result())
	if session == nil {
		t.Fatalf("no %q cookie on the first admin's registration", auth.SessionCookieName)
	}
	if !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteStrictMode || session.Path != "/" {
		t.Errorf("cookie attributes differ from login's: %+v", session)
	}
	if session.Expires.IsZero() || time.Until(session.Expires) > sessionTokenTTL {
		t.Errorf("cookie expiry should be the %s session TTL, got %v", sessionTokenTTL, session.Expires)
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Token != session.Value {
		t.Error("the cookie must carry the session token the response names, as login's does")
	}
	stored, ok := h.tokenStore.(*mockTokenStore).tokens[auth.HashToken(session.Value)]
	if !ok || stored.Scopes != "session" {
		t.Errorf("cookie value is not a stored session token: %+v", stored)
	}
}

// A later account waits for its verification link: no session before that.
func TestRegister_SubsequentUser_GetsNoSessionCookie(t *testing.T) {
	h, raw := firstAdminHandler(t)
	if w := postRegisterWithToken(h, "founder", "founder@x.test", raw); w.Code != http.StatusCreated {
		t.Fatalf("first admin: got %d: %s", w.Code, w.Body.String())
	}
	w := postRegister(h, "member", "member@x.test")
	if w.Code != http.StatusCreated {
		t.Fatalf("member: got %d: %s", w.Code, w.Body.String())
	}
	if c := sessionCookieOf(t, w.Result()); c != nil {
		t.Errorf("an unverified account was signed in: %+v", c)
	}
}
