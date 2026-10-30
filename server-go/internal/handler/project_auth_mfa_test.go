package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-662 step 7: the project's MFA switch, the vault key it needs, and what
// /info tells the auth service.

const (
	testMFA      = testOAuthSettings + "/mfa"
	testMFAKeyAt = "projects/" + testOAuthProject + "/auth/mfa"
)

func (m *memAuthSignInStore) EnableMFA(_ context.Context, projectID string, ensureKey func() error) error {
	if m.err != nil {
		return m.err
	}
	if err := ensureKey(); err != nil {
		return err
	}
	signIn, ok := m.signIn[projectID]
	if !ok {
		signIn = domain.DefaultAuthSignIn()
	}
	signIn.MFAEnabled = true
	m.signIn[projectID] = signIn
	return nil
}

func (m *memAuthSignInStore) DisableMFA(_ context.Context, projectID string) error {
	if m.err != nil {
		return m.err
	}
	if signIn, ok := m.signIn[projectID]; ok {
		signIn.MFAEnabled = false
		m.signIn[projectID] = signIn
	}
	return nil
}

// unreachableVault fails every read the way a sealed or unreachable vault does.
type unreachableVault struct{ *fakeVault }

func (unreachableVault) Get(string) (map[string]string, error) {
	return nil, errors.New("vault sealed")
}

func setupMFAHandler(t *testing.T, on ...features.Feature) oauthFixture {
	t.Helper()
	f := setupSignInHandler(t, on...)
	f.handler.authOAuth.MFA = f.signIn
	f.router.Put("/api/projects/{projectId}/auth-settings/mfa", f.handler.PutAuthMFA)
	return f
}

func TestAuthMFA_EnableCreatesTheKeyThenTheSwitchAndNeverAnswersTheKey(t *testing.T) {
	f := setupMFAHandler(t, features.AuthMFA)
	w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": true})
	body := decodeObject(t, w.Body.Bytes())
	if w.Code != http.StatusOK || body["mfaEnabled"] != true || body["siteUrl"] != "" {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	key := f.vault.data[testMFAKeyAt][domain.MFAKeyField]
	if !domain.ValidMFAKey(key) || len(f.vault.data[testMFAKeyAt]) != 1 {
		t.Fatalf("vault holds %d fields, valid=%v", len(f.vault.data[testMFAKeyAt]), domain.ValidMFAKey(key))
	}
	if strings.Contains(w.Body.String(), key) || !f.signIn.signIn[testOAuthProject].MFAEnabled {
		t.Fatal("the key was answered or the switch not stored")
	}
	if len(f.audit.entries) != 1 || f.audit.entries[0].Action != "auth_mfa.update" ||
		f.audit.entries[0].Details != `{"mfaEnabled":true}` {
		t.Fatalf("audit = %+v", f.audit.entries)
	}
}

func TestAuthMFA_EnableKeepsAnExistingKeyAndOffKeepsItToo(t *testing.T) {
	f := setupMFAHandler(t, features.AuthMFA)
	existing, _ := domain.NewMFAKey()
	f.vault.data[testMFAKeyAt] = map[string]string{domain.MFAKeyField: existing}
	if w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": true}); w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": false})
	if w.Code != http.StatusOK || decodeObject(t, w.Body.Bytes())["mfaEnabled"] != false {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if f.vault.data[testMFAKeyAt][domain.MFAKeyField] != existing || f.signIn.signIn[testOAuthProject].MFAEnabled {
		t.Fatal("the key changed or the switch stayed on")
	}
	if w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": true}); w.Code != http.StatusOK ||
		f.vault.data[testMFAKeyAt][domain.MFAKeyField] != existing {
		t.Fatal("switching back on replaced the key")
	}
	if len(f.audit.entries) != 3 || f.audit.entries[1].Details != `{"mfaEnabled":false}` {
		t.Fatalf("audit = %+v", f.audit.entries)
	}
}

// A key that is there but unreadable is never overwritten: factors may be sealed with it.
func TestAuthMFA_AnInvalidKeyIsRefusedAndLeftAlone(t *testing.T) {
	for _, stored := range []map[string]string{{domain.MFAKeyField: "short"}, {"other": "x"}} {
		f := setupMFAHandler(t, features.AuthMFA)
		f.vault.data[testMFAKeyAt] = stored
		w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": true})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not a valid key") {
			t.Fatalf("stored %v: %d %s", stored, w.Code, w.Body.String())
		}
		if len(f.vault.data[testMFAKeyAt]) != len(stored) || f.vault.data[testMFAKeyAt][domain.MFAKeyField] != stored[domain.MFAKeyField] ||
			f.signIn.signIn[testOAuthProject].MFAEnabled || len(f.audit.entries) != 0 {
			t.Fatalf("stored %v: vault or switch changed", stored)
		}
	}
}

func TestAuthMFA_AnUnreadableVaultNeverMintsAKey(t *testing.T) {
	f := setupMFAHandler(t, features.AuthMFA)
	f.handler.authOAuth.Secrets = unreachableVault{f.vault}
	w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": true})
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "sealed") {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if len(f.vault.data) != 0 || f.signIn.signIn[testOAuthProject].MFAEnabled {
		t.Fatal("a key was written or the switch turned on")
	}
}

func TestAuthMFA_AFailedKeyWriteLeavesTheSwitchOff(t *testing.T) {
	f := setupMFAHandler(t, features.AuthMFA)
	f.handler.authOAuth.Secrets = failingPutSecrets{f.vault}
	if w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": true}); w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT: %d", w.Code)
	}
	if f.signIn.signIn[testOAuthProject].MFAEnabled || len(f.audit.entries) != 0 {
		t.Fatal("the row claims a key the vault does not hold")
	}
}

func TestAuthMFA_StoreFailureIsA500(t *testing.T) {
	f := setupMFAHandler(t, features.AuthMFA)
	f.signIn.err = errors.New("db down at 10.0.0.9")
	w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": false})
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "10.0.0.9") {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
}

func TestAuthMFA_BodyMustBeOneBoolean(t *testing.T) {
	f := setupMFAHandler(t, features.AuthMFA)
	for _, body := range []string{``, `{}`, `{"enabled":"yes"}`, `{"enabled":null}`, `not json`, `{"enabled":true,"key":"x"}`} {
		if w := doRequest(f.router, http.MethodPut, testMFA, body); w.Code != http.StatusBadRequest {
			t.Errorf("%q: %d", body, w.Code)
		}
	}
	if len(f.vault.data) != 0 || len(f.audit.entries) != 0 {
		t.Fatal("a refused body changed something")
	}
}

func TestAuthMFA_SwitchAnswersAsUnmountedWhileTheFlagIsOff(t *testing.T) {
	f := setupMFAHandler(t, features.AuthOAuth, features.AuthGuest, features.AuthUser)
	if w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": true}); w.Code != http.StatusNotFound {
		t.Fatalf("PUT: %d", w.Code)
	}
	if len(f.vault.data) != 0 || f.signIn.signIn[testOAuthProject].MFAEnabled {
		t.Fatal("the flag-off route changed something")
	}
}

func TestAuthMFA_UnwiredIsUnavailable(t *testing.T) {
	f := setupMFAHandler(t, features.AuthMFA)
	f.handler.authOAuth.MFA = nil
	if w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": true}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT: %d", w.Code)
	}
	f = setupMFAHandler(t, features.AuthMFA)
	f.handler.authOAuth.Secrets = nil
	if w := doJSON(f.router, http.MethodPut, testMFA, map[string]any{"enabled": true}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT without a vault: %d", w.Code)
	}
}

func TestAuthMFA_SettingsCarryTheSwitchOnlyWhileTheFlagIs(t *testing.T) {
	for _, flags := range [][]features.Feature{{features.AuthMFA}, {features.AuthMFA, features.AuthOAuth}} {
		f := setupMFAHandler(t, flags...)
		f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true, MFAEnabled: true}
		w := doJSON(f.router, http.MethodGet, testOAuthSettings, nil)
		if w.Code != http.StatusOK || decodeObject(t, w.Body.Bytes())["mfaEnabled"] != true {
			t.Fatalf("%v GET: %d %s", flags, w.Code, w.Body.String())
		}
		w = doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": ""})
		if w.Code != http.StatusOK || decodeObject(t, w.Body.Bytes())["mfaEnabled"] != true || !f.signIn.signIn[testOAuthProject].MFAEnabled {
			t.Fatalf("%v PUT: %d %s", flags, w.Code, w.Body.String())
		}
	}
	off := setupMFAHandler(t, features.AuthOAuth, features.AuthGuest)
	off.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true, MFAEnabled: true}
	assertNoSignInKeys(t, doJSON(off.router, http.MethodGet, testOAuthSettings, nil).Body.Bytes(), "mfaEnabled")
}

func TestAuthMFA_SettingsReadFailureIsA500(t *testing.T) {
	f := setupMFAHandler(t, features.AuthMFA)
	f.signIn.err = errors.New("db down")
	if w := doJSON(f.router, http.MethodGet, testOAuthSettings, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("GET: %d", w.Code)
	}
}

func TestAuthMFA_InfoSaysOnOnlyWithTheFlagAndTheSwitch(t *testing.T) {
	cases := []struct {
		flags  []features.Feature
		stored bool
		want   bool
	}{
		{[]features.Feature{features.AuthMFA}, true, true},
		{[]features.Feature{features.AuthMFA}, false, false},
		{nil, true, false},
		{[]features.Feature{features.AuthOAuth, features.AuthOTP, features.AuthGuest, features.AuthUser}, true, false},
	}
	for _, tc := range cases {
		f := setupMFAHandler(t, tc.flags...)
		f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true, MFAEnabled: tc.stored}
		w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
		info := decodeObject(t, w.Body.Bytes())
		if got, present := info["mfaEnabled"]; tc.want != present || (present && got != true) {
			t.Errorf("flags %v switch %v: mfaEnabled = %v (present %v)", tc.flags, tc.stored, got, present)
		}
	}
	f := setupMFAHandler(t, features.AuthMFA)
	f.signIn.err = errors.New("db down")
	if w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil); w.Code == http.StatusOK {
		t.Fatal("/info answered without knowing the switch")
	}
}
