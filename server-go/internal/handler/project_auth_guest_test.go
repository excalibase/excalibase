package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

const (
	testGuestCaptcha   = testOAuthSettings + "/guest-captcha"
	testGuestCaptchaAt = "projects/" + testOAuthProject + "/auth/captcha"
	testTurnstileKey   = "0x4AAAAAAAtestSecretKey_abc-123"
)

func setupGuestHandler(t *testing.T, on ...features.Feature) oauthFixture {
	t.Helper()
	f := setupSignInHandler(t, on...)
	f.router.Put("/api/projects/{projectId}/auth-settings/guest-captcha", f.handler.PutGuestCaptcha)
	f.router.Delete("/api/projects/{projectId}/auth-settings/guest-captcha", f.handler.DeleteGuestCaptcha)
	return f
}

func TestAuthGuest_InfoCarriesTheSwitchCaptchaAndStableSubjectOnlyWhileTheFlagIs(t *testing.T) {
	f := setupGuestHandler(t, features.AuthGuest)
	f.signIn.signIn[testOAuthProject] = domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true,
		GuestSignInEnabled: true, GuestCaptchaSecretSet: true}
	w := doJSON(f.router, http.MethodGet, testOAuthInfo, nil)
	info := decodeObject(t, w.Body.Bytes())
	if w.Code != http.StatusOK || info["guestSignInEnabled"] != true || info["guestCaptchaEnabled"] != true ||
		info["stableSubject"] != true || info["allowNewUsers"] != true {
		t.Fatalf("flag on: %d %s", w.Code, w.Body.String())
	}
	assertNoSignInKeys(t, w.Body.Bytes(), "emailOtpEnabled", "userApiEnabled")

	for _, flags := range [][]features.Feature{nil, {features.AuthOAuth, features.AuthOTP, features.AuthUser}} {
		off := setupGuestHandler(t, flags...)
		off.signIn.signIn[testOAuthProject] = f.signIn.signIn[testOAuthProject]
		w = doJSON(off.router, http.MethodGet, testOAuthInfo, nil)
		assertNoSignInKeys(t, w.Body.Bytes(), "guestSignInEnabled", "guestCaptchaEnabled", "stableSubject")
		w = doJSON(off.router, http.MethodGet, testOAuthSettings, nil)
		assertNoSignInKeys(t, w.Body.Bytes(), "guestSignInEnabled", "guestCaptchaSecretSet")
	}
}

func TestAuthGuest_SwitchIsSavedAuditedAndKeptWhenAbsent(t *testing.T) {
	f := setupGuestHandler(t, features.AuthGuest)
	w := doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": "", "guestSignInEnabled": true})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"guestSignInEnabled":true`) ||
		!strings.Contains(w.Body.String(), `"guestCaptchaSecretSet":false`) {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if !f.signIn.signIn[testOAuthProject].GuestSignInEnabled {
		t.Fatal("the switch was not stored")
	}
	if len(f.audit.entries) != 1 || !strings.Contains(f.audit.entries[0].Details, `"guestSignInEnabled":true`) {
		t.Fatalf("audit = %+v", f.audit.entries)
	}
	doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": ""})
	if !f.signIn.signIn[testOAuthProject].GuestSignInEnabled {
		t.Fatal("a PUT without the switch turned guests off")
	}
	info := decodeObject(t, doJSON(f.router, http.MethodGet, testOAuthInfo, nil).Body.Bytes())
	if info["guestSignInEnabled"] != true || info["guestCaptchaEnabled"] != nil {
		t.Fatalf("info = %v", info)
	}
}

func TestAuthGuest_SwitchIsIgnoredWhileTheFlagIsOff(t *testing.T) {
	f := setupGuestHandler(t, features.AuthOTP)
	doJSON(f.router, http.MethodPut, testOAuthSettings, map[string]any{"siteUrl": "", "guestSignInEnabled": true})
	if f.signIn.signIn[testOAuthProject].GuestSignInEnabled {
		t.Fatal("the switch was written with authguest off")
	}
}

func TestAuthGuest_CaptchaSecretGoesToTheVaultBeforeTheRowSaysSo(t *testing.T) {
	f := setupGuestHandler(t, features.AuthGuest)
	w := doJSON(f.router, http.MethodPut, testGuestCaptcha, map[string]any{"secretKey": testTurnstileKey})
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), testTurnstileKey) ||
		!strings.Contains(w.Body.String(), `"guestCaptchaSecretSet":true`) {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if f.vault.data[testGuestCaptchaAt]["secret_key"] != testTurnstileKey || !f.signIn.signIn[testOAuthProject].GuestCaptchaSecretSet {
		t.Fatalf("vault=%v row=%+v", f.vault.data[testGuestCaptchaAt] != nil, f.signIn.signIn[testOAuthProject])
	}
	if len(f.audit.entries) != 1 || strings.Contains(f.audit.entries[0].Details, testTurnstileKey) {
		t.Fatalf("audit = %+v", f.audit.entries)
	}

	w = doJSON(f.router, http.MethodDelete, testGuestCaptcha, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"guestCaptchaSecretSet":false`) {
		t.Fatalf("DELETE: %d %s", w.Code, w.Body.String())
	}
	if _, kept := f.vault.data[testGuestCaptchaAt]; kept || f.signIn.signIn[testOAuthProject].GuestCaptchaSecretSet {
		t.Fatal("the secret or the row outlived the delete")
	}
}

func TestAuthGuest_CaptchaSecretIsValidated(t *testing.T) {
	f := setupGuestHandler(t, features.AuthGuest)
	for _, secret := range []string{"", "has space", strings.Repeat("a", 257), "line\nbreak"} {
		if w := doJSON(f.router, http.MethodPut, testGuestCaptcha, map[string]any{"secretKey": secret}); w.Code != http.StatusBadRequest {
			t.Errorf("secret %q: %d", secret, w.Code)
		}
	}
	if len(f.vault.data) != 0 || len(f.audit.entries) != 0 {
		t.Fatal("a refused secret was written")
	}
}

func TestAuthGuest_CaptchaSecretIsNotMarkedSetWhenTheVaultRefuses(t *testing.T) {
	f := setupGuestHandler(t, features.AuthGuest)
	f.handler.authOAuth.Secrets = failingPutSecrets{f.vault}
	if w := doJSON(f.router, http.MethodPut, testGuestCaptcha, map[string]any{"secretKey": testTurnstileKey}); w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT: %d", w.Code)
	}
	if f.signIn.signIn[testOAuthProject].GuestCaptchaSecretSet {
		t.Fatal("the row claims a secret the vault does not hold")
	}
}
