package handler

import (
	"errors"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-457: the new-user switch must reach auth and Studio with every sign-in
// flag off, or no project can close registration.

func TestInfo_FlagsOffCarriesAllowNewUsersFalse(t *testing.T) {
	f := setupSignInHandler(t)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{"https://a.example.com/cb"}, AllowNewUsers: false}
	w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("info: %d %s", w.Code, w.Body.String())
	}
	body := decodeObject(t, w.Body.Bytes())
	if got, ok := body["allowNewUsers"]; !ok || got != false {
		t.Fatalf("allowNewUsers = %v (present %v): %s", got, ok, w.Body.String())
	}
	assertNoSignInKeys(t, w.Body.Bytes(), "redirectUrls", "oauthProviders", "emailOtpEnabled", "guestSignInEnabled")
}

func TestInfo_FlagsOffDefaultsAllowNewUsersTrue(t *testing.T) {
	f := setupSignInHandler(t)
	w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil)
	body := decodeObject(t, w.Body.Bytes())
	if got, ok := body["allowNewUsers"]; !ok || got != true {
		t.Fatalf("allowNewUsers = %v (present %v): %s", got, ok, w.Body.String())
	}
}

func TestInfo_FlagsOffUnreadablePolicyIs503(t *testing.T) {
	f := setupSignInHandler(t)
	f.signIn.err = errors.New("pq: connection reset")
	if w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", w.Code)
	}
}

func TestInfo_FlagOnCarriesStoredAllowNewUsersFalse(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: false}
	w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil)
	body := decodeObject(t, w.Body.Bytes())
	if body["allowNewUsers"] != false || body["redirectUrls"] == nil || body["oauthProviders"] == nil {
		t.Fatalf("info = %s", w.Body.String())
	}
}

func TestAuthSettings_FlagsOffAcceptAndReportAllowNewUsers(t *testing.T) {
	f := setupSignInHandler(t)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{"https://a.example.com/cb"}, AllowNewUsers: true, EmailOTPEnabled: true}
	w := doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": "", "allowNewUsers": false, "redirectUrls": []string{}})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	got := f.signIn.signIn[testOAuthProject]
	if got.AllowNewUsers || len(got.RedirectURLs) != 1 || !got.EmailOTPEnabled {
		t.Fatalf("stored = %+v: only allowNewUsers may change with the flags off", got)
	}
	if body := decodeObject(t, w.Body.Bytes()); body["allowNewUsers"] != false {
		t.Fatalf("PUT answer = %s", w.Body.String())
	}
	w = doJSON(f.router, http.MethodGet, testOAuthSettings, nil)
	body := decodeObject(t, w.Body.Bytes())
	if body["allowNewUsers"] != false {
		t.Fatalf("GET = %s", w.Body.String())
	}
	assertNoSignInKeys(t, w.Body.Bytes(), "redirectUrls", "emailOtpEnabled")
}

func TestAuthSettings_FlagsOffPutWithoutSwitchKeepsIt(t *testing.T) {
	f := setupSignInHandler(t)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: false}
	doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": "https://app.example.com"})
	if f.signIn.signIn[testOAuthProject].AllowNewUsers {
		t.Fatal("an older Studio's PUT reopened registration")
	}
}
