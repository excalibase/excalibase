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
			for _, path := range []string{"/api/admin/analytics/users", "/api/admin/analytics/abuse", "/api/admin/analytics/traffic"} {
				if code := matrixRequest(router, http.MethodGet, path, admins[callerAdminSession]); code != tc.want {
					t.Errorf("%s with the flag %s = %d, want %d", path, tc.name, code, tc.want)
				}
			}
		})
	}
}
