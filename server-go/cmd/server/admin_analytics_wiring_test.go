package main

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
)

func TestAdminAnalytics_RoutesFollowTheirFlagOnTheRealRouter(t *testing.T) {
	cases := []struct {
		name  string
		flags features.Flags
		want  int
	}{
		{"off", features.NewStatic(), http.StatusNotFound},
		{"on", features.NewStatic(features.AdminAnalytics), http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, _, admins := buildMatrixWith(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps) {
				deps.adminAnalyticsHandler = handler.NewAdminAnalyticsHandler(matrixAnalytics{}, tc.flags)
			})
			for _, path := range []string{"/api/admin/analytics/users", "/api/admin/analytics/abuse", "/api/admin/analytics/traffic", "/api/admin/analytics/credits"} {
				if code := matrixRequest(router, http.MethodGet, path, admins[callerAdminSession]); code != tc.want {
					t.Errorf("%s with the flag %s = %d, want %d", path, tc.name, code, tc.want)
				}
			}
		})
	}
}

var adminAnalyticsPaths = []string{"/api/admin/analytics/users", "/api/admin/analytics/abuse", "/api/admin/analytics/traffic", "/api/admin/analytics/credits"}

func analyticsOn(deps *handlerDeps) {
	deps.adminAnalyticsHandler = handler.NewAdminAnalyticsHandler(matrixAnalytics{}, features.NewStatic(features.AdminAnalytics))
}

// Whoever is not a platform admin's own session meets the unmounted route's answer, so the
// analytics routes are not even confirmed to exist.
func TestAdminAnalytics_RoutesAreStaffOnlyAndOtherwiseLookUnmounted(t *testing.T) {
	router, who, admins := buildMatrixWith(t, config.AppConfig{DeploymentMode: "selfhosted"}, analyticsOn)
	for _, path := range adminAnalyticsPaths {
		if code, _ := matrixBody(router, path, admins[callerAdminSession]); code != http.StatusOK {
			t.Errorf("%s as the admin's session = %d, want 200", path, code)
		}
		if code, _ := matrixBody(router, path, ""); code != http.StatusUnauthorized {
			t.Errorf("%s anonymous = %d, want 401", path, code)
		}
	}
	others := map[string]string{
		callerAdminReadOnly: admins[callerAdminReadOnly], callerAdminWrite: admins[callerAdminWrite],
		callerDeveloper: who[callerDeveloper], callerOtherOrg: who[callerOtherOrg],
	}
	for name, token := range others {
		wantCode, wantBody := matrixBody(router, "/api/admin/nothing-mounted-here", token)
		for _, path := range adminAnalyticsPaths {
			if code, body := matrixBody(router, path, token); code != wantCode || body != wantBody {
				t.Errorf("%s on %s = %d %q, want the unmounted %d %q", name, path, code, body, wantCode, wantBody)
			}
		}
	}
}

// Whatever the caller sends, status, headers and body match an unmounted route, for every method.
func TestAdminAnalytics_NoMethodOrCredentialSeparatesTheRoutesFromAnUnmountedOne(t *testing.T) {
	router, who, admins, _ := buildMatrixPlatform(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps, platform *fakePlatform) {
		analyticsOn(deps)
		withStaffRoles(deps, platform)
	})
	tokens := map[string]string{
		callerAdminReadOnly: admins[callerAdminReadOnly], callerAdminWrite: admins[callerAdminWrite],
		callerDeveloper: who[callerDeveloper], callerOtherOrg: who[callerOtherOrg],
		callerBoundElsewhere: who[callerBoundElsewhere], callerReadOnly: who[callerReadOnly],
		callerOperator: staffSessionToken(matrixOperatorID), callerViewer: staffSessionToken(matrixViewerID),
	}
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	for name, token := range tokens {
		for _, method := range methods {
			want := matrixAnswer(router, method, "/api/admin/zz", token)
			for _, path := range adminAnalyticsPaths {
				if got := matrixAnswer(router, method, path, token); got != want {
					t.Errorf("%s %s as %s = %+v, want the unmounted %+v", method, path, name, got, want)
				}
			}
		}
	}
}
