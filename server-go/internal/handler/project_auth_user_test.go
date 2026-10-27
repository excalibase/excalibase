package handler

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-662: /info tells the auth service account self-service is on, and says
// nothing at all while it is off.
func TestAuthUser_InfoSaysTheUserAPIIsOnOnlyWhileTheFlagIs(t *testing.T) {
	on := setupSignInHandler(t, features.AuthUser)
	w := doJSON(on.router, http.MethodGet, testOAuthInfo, nil)
	if w.Code != http.StatusOK || decodeObject(t, w.Body.Bytes())["userApiEnabled"] != true {
		t.Fatalf("flag on: %d %s", w.Code, w.Body.String())
	}
	assertNoSignInKeys(t, w.Body.Bytes(), "redirectUrls", "allowNewUsers", "oauthProviders", "emailOtpEnabled")

	for _, flags := range [][]features.Feature{nil, {features.AuthOAuth, features.AuthOTP}} {
		off := setupSignInHandler(t, flags...)
		w = doJSON(off.router, http.MethodGet, testOAuthInfo, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("flag off: %d", w.Code)
		}
		assertNoSignInKeys(t, w.Body.Bytes(), "userApiEnabled")
	}
}
