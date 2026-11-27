package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/studiooauth"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

type recordedSignup struct{ terms, grants []string }

func (r *recordedSignup) RecordTerms(_ context.Context, userID, version string) {
	r.terms = append(r.terms, userID+"="+version)
}

func (r *recordedSignup) GrantOnVerified(_ context.Context, userID string) {
	r.grants = append(r.grants, userID)
}

func TestVerificationGrantsOnEveryPathThatProvesAnAddress(t *testing.T) {
	verifier, store, sender := newTestVerifier()
	signup := &recordedSignup{}
	verifier.SetCreditSignup(signup)
	store.users["u1"] = "dev@example.com"
	if err := verifier.Send(context.Background(), &domain.User{ID: "u1", Email: "dev@example.com"}); err != nil {
		t.Fatal(err)
	}
	token := sender.lastToken(t)
	if _, err := verifier.Confirm(context.Background(), "wrong-token"); err == nil {
		t.Fatal("a wrong token confirmed")
	}
	if len(signup.grants) != 0 {
		t.Fatalf("a refused link must not grant: %v", signup.grants)
	}
	if _, err := verifier.Confirm(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	store.users["u2"] = "other@example.com"
	if err := verifier.MarkVerified(context.Background(), "u2"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(signup.grants, ",") != "u1,u2" {
		t.Fatalf("grants %v, want u1 by link and u2 by an admin", signup.grants)
	}
}

func TestVerificationWithNoCreditsWiredStillWorks(t *testing.T) {
	verifier, store, _ := newTestVerifier()
	store.users["u2"] = "other@example.com"
	if err := verifier.MarkVerified(context.Background(), "u2"); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterRecordsTheTermsTheBoxCarried(t *testing.T) {
	h := registerHandler(seededStore(), &inviteOrgStore{}, false)
	signup := &recordedSignup{}
	h.SetCreditSignup(signup)
	body := func(name, extra string) string {
		return fmt.Sprintf(`{"username":%q,"email":%q,"password":%q%s}`, name, name+"@company.test", testutil.FixturePassword("reg-"+name), extra)
	}
	if w := doRegisterRequest(h, body("ticked", `,"creditTermsVersion":"v1"`)); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := doRegisterRequest(h, body("unticked", "")); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(signup.terms) != 1 || !strings.HasSuffix(signup.terms[0], "=v1") {
		t.Fatalf("only the person who ticked the box is recorded: %v", signup.terms)
	}
}

func TestOAuthCarriesTheTermsBoxThroughTheProvider(t *testing.T) {
	h := newOAuthHarness(t, false)
	signup := &recordedSignup{}
	h.auth.SetCreditSignup(signup)
	h.signIn.identity = studiooauth.Identity{Provider: "github", Subject: "9999", Email: "brand-new@example.com"}
	w := doRequest(h.router, "GET", "/api/auth/oauth/github/start?creditTerms=v1", "")
	var carried *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName(creditTermsCookie) {
			carried = c
		}
	}
	if carried == nil || carried.Value != "v1" || !carried.HttpOnly || carried.SameSite != http.SameSiteLaxMode || carried.MaxAge <= 0 {
		t.Fatalf("terms cookie: %+v", carried)
	}

	req := httptest.NewRequest("GET", "/api/auth/oauth/github/callback?code=c&state=st4te", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName(oauthStateCookie), Value: "st4te"})
	req.AddCookie(&http.Cookie{Name: auth.CookieName(creditTermsCookie), Value: "v1"})
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	redirectedTo(t, rec)
	if len(signup.terms) != 1 || len(signup.grants) != 1 {
		t.Fatalf("terms %v grants %v", signup.terms, signup.grants)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName(creditTermsCookie) {
			cleared = c.MaxAge < 0
		}
	}
	if !cleared {
		t.Fatal("the terms cookie must be cleared once it is used")
	}
}

func TestOAuthWithoutTheBoxRecordsNoTermsButStillTriesTheGrant(t *testing.T) {
	h := newOAuthHarness(t, false)
	signup := &recordedSignup{}
	h.auth.SetCreditSignup(signup)
	redirectedTo(t, h.callback("st4te"))
	if len(signup.terms) != 0 || len(signup.grants) != 1 {
		t.Fatalf("terms %v grants %v", signup.terms, signup.grants)
	}
}

func TestOAuthStartWithoutTheBoxSetsNoTermsCookie(t *testing.T) {
	h := newOAuthHarness(t, false)
	w := doRequest(h.router, "GET", "/api/auth/oauth/github/start", "")
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName(creditTermsCookie) {
			t.Fatalf("unexpected cookie %+v", c)
		}
	}
}

func callbackCarryingTerms(h *oauthHarness) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/auth/oauth/github/callback?code=c&state=st4te", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName(oauthStateCookie), Value: "st4te"})
	req.AddCookie(&http.Cookie{Name: auth.CookieName(creditTermsCookie), Value: "v1"})
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// A start link anyone can craft must not record consent for an account that was not made through it.
func TestOAuthSignInOfAnExistingAccountRecordsNoTermsEvenWithTheCookie(t *testing.T) {
	h := newOAuthHarness(t, false)
	signup := &recordedSignup{}
	h.auth.SetCreditSignup(signup)
	h.users.users["u-dev"] = &domain.User{ID: "u-dev", Username: "dev", Email: "dev@example.com", Active: true, Kind: domain.UserKindHuman}
	h.links.users["u-dev"] = "dev@example.com"

	redirectedTo(t, callbackCarryingTerms(h))

	if len(signup.terms) != 0 {
		t.Fatalf("terms recorded for an existing account: %v", signup.terms)
	}
	if len(signup.grants) != 1 {
		t.Fatalf("the grant is still tried: %v", signup.grants)
	}
}
