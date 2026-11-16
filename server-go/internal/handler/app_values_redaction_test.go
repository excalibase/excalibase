package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

const plainAPIKey = "sk_live_pasted_by_the_owner"

func seedAppWithPlainKey(t *testing.T, role string) (appsPath string, serve func(method, path string) *httptest.ResponseRecorder) {
	t.Helper()
	r, store, _ := setupAppRouterAsRole(t, newFakeSources("storefront_db"), features.NewStatic(features.All()...), role)
	value := plainAPIKey
	store.apps[appKey("proj-a", "app-1")] = apphost.App{
		ID: "app-1", ProjectID: "proj-a", Name: "web", Image: "nginx:1", Port: 8080, Replicas: 1, Version: 1,
		Env: []apphost.EnvVar{{Name: "STRIPE_KEY", Kind: apphost.KindLiteral, Value: &value}},
	}
	return "/api/projects/proj-a/apps", func(method, path string) *httptest.ResponseRecorder {
		return doAppRequest(t, r, method, path, nil)
	}
}

// EXC-761: a Viewer reads the app but never what its plain variables hold.
func TestAppReadsHideVariableValuesFromAViewer(t *testing.T) {
	base, serve := seedAppWithPlainKey(t, domain.OrgRoleViewer)
	for name, path := range map[string]string{"list": base + "/", "get": base + "/app-1/"} {
		t.Run(name, func(t *testing.T) {
			w := serve(http.MethodGet, path)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), plainAPIKey) {
				t.Fatalf("a Viewer read a plain variable value: %s", w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "STRIPE_KEY") {
				t.Fatalf("the variable name must still be listed: %s", w.Body.String())
			}
		})
	}
}

// With no resolved access the answer fails closed.
func TestAppReadsHideVariableValuesWhenAccessIsUnresolved(t *testing.T) {
	base, serve := seedAppWithPlainKey(t, "")
	if w := serve(http.MethodGet, base+"/app-1/"); strings.Contains(w.Body.String(), plainAPIKey) {
		t.Fatalf("an unresolved caller read a value: %s", w.Body.String())
	}
}

func TestAppReadsShowVariableValuesToADeveloper(t *testing.T) {
	base, serve := seedAppWithPlainKey(t, domain.OrgRoleDeveloper)
	if w := serve(http.MethodGet, base+"/app-1/"); !strings.Contains(w.Body.String(), plainAPIKey) {
		t.Fatalf("a Developer must read the values they set: %s", w.Body.String())
	}
}
