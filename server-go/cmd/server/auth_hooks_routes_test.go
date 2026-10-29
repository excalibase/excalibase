package main

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-662: a Developer reads the hooks but never generates or removes the
// signing secret, nor points a hook anywhere.
func TestAuthHooks_DeveloperCannotChangeTheSecretOrAHook(t *testing.T) {
	router, developer := darkFeatureRouter(t, features.AuthHooks)
	base := "/api/projects/" + matrixProjectA + "/auth-hooks"
	for _, probe := range []darkProbe{
		{http.MethodPost, base + "/secret"},
		{http.MethodDelete, base + "/secret"},
		{http.MethodPut, base + "/send_email"},
	} {
		if w := probeDark(router, probe, developer); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a Developer: %d %s", probe.method, probe.path, w.Code, w.Body.String())
		}
	}
	if w := probeDark(router, darkProbe{http.MethodGet, base}, developer); w.Code == http.StatusForbidden || w.Code == http.StatusNotFound {
		t.Errorf("GET as a Developer: %d", w.Code)
	}
}
