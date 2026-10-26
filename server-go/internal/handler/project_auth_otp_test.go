package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

func TestAuthOTP_AloneCarriesTheSignInPolicyAndItsSwitch(t *testing.T) {
	f := setupSignInHandler(t, features.AuthOTP)
	w := doJSON(f.router, http.MethodGet, testOAuthSettings, nil)
	for _, want := range []string{`"redirectUrls":[]`, `"allowNewUsers":true`, `"emailOtpEnabled":false`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("auth-settings lacks %s: %s", want, w.Body.String())
		}
	}
	w = doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{
		"siteUrl": "https://app.example.com", "redirectUrls": []string{"https://app.example.com/cb"}, "emailOtpEnabled": true})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"emailOtpEnabled":true`) {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if got := f.signIn.signIn[testOAuthProject]; !got.EmailOTPEnabled || len(got.RedirectURLs) != 1 || !got.AllowNewUsers {
		t.Fatalf("stored = %+v", got)
	}
	if len(f.audit.entries) != 1 || !strings.Contains(f.audit.entries[0].Details, `"emailOtpEnabled":true`) {
		t.Fatalf("audit = %+v", f.audit.entries)
	}

	w = doJSON(f.router, http.MethodGet, testOAuthInfo, nil)
	info := decodeObject(t, w.Body.Bytes())
	if info["emailOtpEnabled"] != true || info["allowNewUsers"] != true || len(info["redirectUrls"].([]any)) != 1 {
		t.Fatalf("info = %s", w.Body.String())
	}
	if providers, _ := info["oauthProviders"].([]any); len(providers) != 0 {
		t.Fatalf("providers offered with authoauth off: %s", w.Body.String())
	}
}

func TestAuthOTP_AbsentSwitchKeepsTheStoredValue(t *testing.T) {
	f := setupSignInHandler(t, features.AuthOTP)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true, EmailOTPEnabled: true}
	if w := doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": "https://b.example.com"}); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if !f.signIn.signIn[testOAuthProject].EmailOTPEnabled {
		t.Fatal("a PUT without the switch turned email sign-in off")
	}
}

func TestAuthOTP_OffHidesTheSwitchEvenWithOAuthOn(t *testing.T) {
	f := setupSignInHandler(t, features.AuthOAuth)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true, EmailOTPEnabled: true}
	w := doJSON(f.router, http.MethodGet, testOAuthSettings, nil)
	assertNoSignInKeys(t, w.Body.Bytes(), "emailOtpEnabled")
	w = doJSON(f.router, http.MethodGet, testOAuthInfo, nil)
	assertNoSignInKeys(t, w.Body.Bytes(), "emailOtpEnabled")

	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true}
	doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": "", "emailOtpEnabled": true})
	if f.signIn.signIn[testOAuthProject].EmailOTPEnabled {
		t.Fatal("the switch was written with authotp off")
	}
}

func TestAuthOTP_BothOffKeepTodaysShape(t *testing.T) {
	f := setupSignInHandler(t)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{EmailOTPEnabled: true}
	for _, path := range []string{testOAuthSettings, testOAuthInfo} {
		w := doJSON(f.router, http.MethodGet, path, nil)
		assertNoSignInKeys(t, w.Body.Bytes(), "emailOtpEnabled", "redirectUrls", "allowNewUsers", "oauthProviders")
	}
}
