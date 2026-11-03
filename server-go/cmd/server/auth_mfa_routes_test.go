package main

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-662: switching end-user MFA and resetting a user's factors are an
// Admin's, as the guest captcha and end-user deletion are.
func TestAuthMFA_DeveloperCannotSwitchMFAOrResetFactors(t *testing.T) {
	router, developer := darkFeatureRouter(t, features.AuthMFA)
	for _, probe := range []darkProbe{
		{http.MethodPut, "/api/projects/" + matrixProjectA + "/auth-settings/mfa"},
		{http.MethodDelete, "/api/projects/" + matrixProjectA + "/end-users/7/factors"},
	} {
		if w := probeDark(router, probe, developer); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a Developer: %d %s", probe.method, probe.path, w.Code, w.Body.String())
		}
	}
}
