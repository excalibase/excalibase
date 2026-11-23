package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/excalibase/provisioning-poc/internal/features"
	"net/http"
	"net/http/httptest"
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

func TestAuthSettings_FlagsOffPolicyStoreFailuresAre500(t *testing.T) {
	f := setupSignInHandler(t)
	f.signIn.err = errors.New("pq: connection reset")
	if w := doJSON(f.router, http.MethodGet, testOAuthSettings, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("GET: %d", w.Code)
	}
	for name, body := range map[string]map[string]any{
		"with the switch":    {"siteUrl": "", "allowNewUsers": false},
		"without the switch": {"siteUrl": ""},
	} {
		if w := doJSON(f.router, http.MethodPut, testOAuthSettings, body); w.Code != http.StatusInternalServerError {
			t.Fatalf("PUT %s: %d", name, w.Code)
		}
	}
}

func TestAuthSettings_FlagsOffWithoutSignInStoreReadsOpenAndKeepsToday(t *testing.T) {
	f := setupSignInHandler(t)
	f.handler.SetAuthOAuth(AuthOAuthDeps{Flags: f.handler.authOAuth.Flags, Providers: f.providers, Secrets: f.vault, PublicBaseURL: testOAuthBaseURL})
	for _, path := range []string{testOAuthInfo, testOAuthSettings} {
		w := doJSON(f.router, http.MethodGet, path, nil)
		if w.Code != http.StatusOK || decodeObject(t, w.Body.Bytes())["allowNewUsers"] != true {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": "https://app.example.com", "allowNewUsers": false})
	if w.Code != http.StatusOK || f.settings.settings[testOAuthProject].SiteURL != "https://app.example.com" {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
}

func TestAuthSettings_FlagsOffPutRefusesNothingTheSwitchBreaks(t *testing.T) {
	f := setupSignInHandler(t)
	f.settings.err = errors.New("pq: connection reset")
	if w := doJSON(f.router, http.MethodGet, testOAuthSettings, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("GET: %d", w.Code)
	}
}

func putAs(f oauthFixture, role string, body map[string]any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, testOAuthSettings, bytes.NewReader(raw))
	req.Header.Set(testRoleHeader, role)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

// With the flags off the route needs only Developer, so the switch carries its own Admin rule.
func TestAuthSettings_ChangingAllowNewUsersNeedsAdminWhateverTheFlags(t *testing.T) {
	for name, flags := range map[string][]features.Feature{"flags off": nil, "flags on": {features.AuthOAuth}} {
		t.Run(name, func(t *testing.T) {
			f := setupSignInHandler(t, flags...)
			f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true}

			if w := putAs(f, domain.OrgRoleDeveloper, map[string]any{"siteUrl": "", "allowNewUsers": false}); w.Code != http.StatusForbidden {
				t.Fatalf("developer closing sign-ups: %d %s", w.Code, w.Body.String())
			}
			if !f.signIn.signIn[testOAuthProject].AllowNewUsers {
				t.Fatal("a refused PUT changed the switch")
			}
			if w := putAs(f, domain.OrgRoleDeveloper, map[string]any{"siteUrl": "https://a.example.com", "allowNewUsers": true}); w.Code != http.StatusOK {
				t.Fatalf("developer sending the stored value: %d %s", w.Code, w.Body.String())
			}
			if w := putAs(f, domain.OrgRoleDeveloper, map[string]any{"siteUrl": "https://b.example.com"}); w.Code != http.StatusOK {
				t.Fatalf("developer omitting the switch: %d %s", w.Code, w.Body.String())
			}
			if w := putAs(f, domain.OrgRoleAdmin, map[string]any{"siteUrl": "", "allowNewUsers": false}); w.Code != http.StatusOK {
				t.Fatalf("admin: %d %s", w.Code, w.Body.String())
			}
			if f.signIn.signIn[testOAuthProject].AllowNewUsers {
				t.Fatal("the admin's change was not stored")
			}
			// Reopening is a change too.
			if w := putAs(f, domain.OrgRoleDeveloper, map[string]any{"siteUrl": "", "allowNewUsers": true}); w.Code != http.StatusForbidden {
				t.Fatalf("developer reopening sign-ups: %d", w.Code)
			}
		})
	}
}

func TestAuthSettings_AllowNewUsersRoleCheckFailsClosedOnAnUnreadablePolicy(t *testing.T) {
	f := setupSignInHandler(t)
	f.signIn.err = errors.New("pq: connection reset")
	if w := putAs(f, domain.OrgRoleDeveloper, map[string]any{"siteUrl": "", "allowNewUsers": false}); w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d", w.Code)
	}
}
