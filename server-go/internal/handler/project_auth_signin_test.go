package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	testOAuthSettings = "/api/projects/" + testOAuthProject + "/auth-settings"
	testOAuthInfo     = "/api/projects/" + testOAuthProject + "/info"
)

func decodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return out
}

func assertNoSignInKeys(t *testing.T, body []byte, keys ...string) {
	t.Helper()
	got := decodeObject(t, body)
	for _, key := range keys {
		if _, present := got[key]; present {
			t.Errorf("%s present with the flag off: %s", key, body)
		}
	}
}

func TestAuthSettings_FlagOffKeepsTodaysShape(t *testing.T) {
	f := setupOAuthHandler(t, false)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{"https://a.example.com/cb"}, AllowNewUsers: false}
	w := doJSON(f.router, http.MethodGet, testOAuthSettings, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d", w.Code)
	}
	assertNoSignInKeys(t, w.Body.Bytes(), "redirectUrls", "allowNewUsers")

	w = doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{
		"siteUrl": "https://app.example.com", "redirectUrls": []string{}, "allowNewUsers": true})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	assertNoSignInKeys(t, w.Body.Bytes(), "redirectUrls", "allowNewUsers")
	if got := f.signIn.signIn[testOAuthProject]; got.AllowNewUsers || len(got.RedirectURLs) != 1 {
		t.Fatalf("the flag-off PUT wrote the policy: %+v", got)
	}
	if f.settings.settings[testOAuthProject].SiteURL != "https://app.example.com" {
		t.Fatal("the flag-off PUT did not store the site URL")
	}
}

func TestAuthSettings_FlagOnReportsTheDefaults(t *testing.T) {
	f := setupOAuthHandler(t, true)
	w := doJSON(f.router, http.MethodGet, testOAuthSettings, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"redirectUrls":[]`) || !strings.Contains(w.Body.String(), `"allowNewUsers":true`) {
		t.Fatalf("GET = %s", w.Body.String())
	}
}

func TestAuthSettings_FlagOnPutNormalisesAndStores(t *testing.T) {
	f := setupOAuthHandler(t, true)
	w := doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{
		"siteUrl":       "https://app.example.com",
		"redirectUrls":  []string{" HTTPS://App.Example.com/cb", "https://app.example.com/cb", "http://localhost:3000/cb"},
		"allowNewUsers": false,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	got := f.signIn.signIn[testOAuthProject]
	if got.AllowNewUsers || fmt.Sprint(got.RedirectURLs) != "[https://app.example.com/cb http://localhost:3000/cb]" {
		t.Fatalf("stored = %+v", got)
	}
	body := decodeObject(t, w.Body.Bytes())
	if body["allowNewUsers"] != false || len(body["redirectUrls"].([]any)) != 2 || body["siteUrl"] != "https://app.example.com" {
		t.Fatalf("response = %v", body)
	}
}

// An older Studio sends only the original two fields; they must not wipe the policy.
func TestAuthSettings_FlagOnAbsentFieldsKeepTheStoredPolicy(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{"https://a.example.com/cb"}, AllowNewUsers: false}
	w := doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": "https://b.example.com", "requireEmailVerification": true})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	got := f.signIn.signIn[testOAuthProject]
	if got.AllowNewUsers || fmt.Sprint(got.RedirectURLs) != "[https://a.example.com/cb]" {
		t.Fatalf("policy changed: %+v", got)
	}
	if s := f.settings.settings[testOAuthProject]; s.SiteURL != "https://b.example.com" || !s.RequireEmailVerification {
		t.Fatalf("settings = %+v", s)
	}
	// Only one field given: the other keeps its stored value.
	doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": "https://b.example.com", "redirectUrls": []string{}})
	if got := f.signIn.signIn[testOAuthProject]; got.AllowNewUsers || len(got.RedirectURLs) != 0 {
		t.Fatalf("after clearing the list: %+v", got)
	}
}

func TestAuthSettings_FlagOnRefusesBadRedirectURLs(t *testing.T) {
	tooMany := make([]string, 0, domain.MaxRedirectURLs+1)
	for i := 0; i <= domain.MaxRedirectURLs; i++ {
		tooMany = append(tooMany, fmt.Sprintf("https://app%d.example.com", i))
	}
	for name, list := range map[string][]string{
		"query": {"https://a.example.com/cb?next=1"}, "empty": {""}, "plain http": {"http://a.example.com"}, "too many": tooMany,
	} {
		t.Run(name, func(t *testing.T) {
			f := setupOAuthHandler(t, true)
			w := doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": "", "redirectUrls": list})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d (%s)", w.Code, w.Body.String())
			}
			if _, written := f.signIn.signIn[testOAuthProject]; written {
				t.Fatal("a refused PUT was stored")
			}
		})
	}
}

func TestAuthSettings_FlagOnPolicyStoreFailures(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.signIn.err = errors.New("pq: connection reset")
	if w := doJSON(f.router, http.MethodGet, testOAuthSettings, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("GET: %d", w.Code)
	}
	if w := doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": ""}); w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT: %d", w.Code)
	}
}

func TestInfo_FlagOffCarriesNoOAuthKeys(t *testing.T) {
	f := setupOAuthHandler(t, false)
	f.providers.rows[testOAuthProject+"/github"] = domain.ProjectAuthProvider{Provider: "github", Enabled: true, ClientID: "gh", ClientSecretSet: true}
	w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("info: %d %s", w.Code, w.Body.String())
	}
	assertNoSignInKeys(t, w.Body.Bytes(), "redirectUrls", "allowNewUsers", "oauthProviders")
}

func TestInfo_FlagOnDefaultsWithoutRows(t *testing.T) {
	f := setupOAuthHandler(t, true)
	w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("info: %d %s", w.Code, w.Body.String())
	}
	for _, want := range []string{`"redirectUrls":[]`, `"allowNewUsers":true`, `"oauthProviders":[]`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("info lacks %s: %s", want, w.Body.String())
		}
	}
}

func TestInfo_FlagOnListsOnlyUsableProviders(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{"https://app.example.com/auth/callback"}, AllowNewUsers: true}
	f.providers.rows[testOAuthProject+"/google"] = domain.ProjectAuthProvider{Provider: "google", Enabled: true, ClientID: "g", ClientSecretSet: false}
	f.providers.rows[testOAuthProject+"/github"] = domain.ProjectAuthProvider{Provider: "github", Enabled: true, ClientID: "gh", ClientSecretSet: true}
	w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil)
	var info struct {
		RedirectURLs   []string                   `json:"redirectUrls"`
		AllowNewUsers  bool                       `json:"allowNewUsers"`
		OAuthProviders []domain.OAuthProviderInfo `json:"oauthProviders"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []domain.OAuthProviderInfo{{Provider: "github", ClientID: "gh", CallbackURL: testOAuthCallback}}
	if fmt.Sprint(info.OAuthProviders) != fmt.Sprint(want) || !info.AllowNewUsers ||
		fmt.Sprint(info.RedirectURLs) != "[https://app.example.com/auth/callback]" {
		t.Fatalf("info = %+v (%s)", info, w.Body.String())
	}
}

func TestInfo_FlagOnUnreadablePolicyIs503(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.providers.err = errors.New("pq: connection reset")
	if w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("providers unreadable: %d", w.Code)
	}
	f.providers.err = nil
	f.signIn.err = errors.New("pq: connection reset")
	if w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("policy unreadable: %d", w.Code)
	}
}

// The redirect list decides where sign-in codes are delivered, so every change is audited.
func TestAuthSettings_FlagOnPutAuditsTheSignInPolicy(t *testing.T) {
	f := setupOAuthHandler(t, true)
	w := doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{
		"siteUrl": "https://app.example.com", "redirectUrls": []string{"https://app.example.com/cb"}, "allowNewUsers": false,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if len(f.audit.entries) != 1 {
		t.Fatalf("audit entries = %d", len(f.audit.entries))
	}
	entry := f.audit.entries[0]
	if entry.Action != "auth_settings.update" || entry.ProjectID != testOAuthProject ||
		!strings.Contains(entry.Details, "https://app.example.com/cb") || !strings.Contains(entry.Details, `"allowNewUsers":false`) {
		t.Fatalf("audit = %+v", entry)
	}
}
