package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

// EXC-554: what shipped dark answers exactly as a route that was never
// mounted, and answers normally once its feature is on.

// unmountedBody is what chi, and features.Require, answer for a route that is not there.
const unmountedBody = "404 page not found\n"

type darkProbe struct{ method, path string }

// darkRoutes is every route a feature hides. A declared feature with no
// probe here fails TestDarkRoutes_EveryFeatureIsWired.
var darkRoutes = map[features.Feature][]darkProbe{
	features.AppGitBuilds: {
		{http.MethodPost, "/api/projects/" + matrixProjectA + "/internal-registry/pull-credential/rotate"},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/apps/app-1/git"},
		{http.MethodPut, "/api/projects/" + matrixProjectA + "/apps/app-1/git"},
		{http.MethodPost, "/api/projects/" + matrixProjectA + "/apps/app-1/git/sign-in"},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/apps/app-1/git/repositories"},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/apps/app-1/builds"},
		{http.MethodPost, "/api/projects/" + matrixProjectA + "/apps/app-1/builds"},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/apps/app-1/builds/b-1"},
	},
	features.MCP: {
		{http.MethodPost, "/mcp"},
		{http.MethodGet, "/api/schema/" + matrixProjectA + "/query?sql=select+1"},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/ai-activity/"},
	},
	features.Pipeline: {
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/apps/app-1/deploys/dep-1"},
	},
	features.MySQL: {
		{http.MethodGet, "/api/mysql/catalog"},
	},
	features.SlowQueries: {
		{http.MethodGet, "/api/provision/" + matrixProjectA + "/slow-queries/"},
		{http.MethodPost, "/api/provision/" + matrixProjectA + "/slow-queries/reset"},
		{http.MethodPost, "/api/provision/" + matrixProjectA + "/slow-queries/enable"},
	},
	features.RequestMetrics: {
		{http.MethodGet, "/api/provision/" + matrixProjectA + "/requests?range=1h"},
	},
	features.Advisors: {
		{http.MethodGet, "/api/provision/" + matrixProjectA + "/advisors"},
	},
	features.MetricsEndpoint: {
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/metrics"},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/metrics-keys"},
		{http.MethodPost, "/api/projects/" + matrixProjectA + "/metrics-keys"},
		{http.MethodDelete, "/api/projects/" + matrixProjectA + "/metrics-keys/key-1"},
	},
	features.LogDrains: {
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/log-drains"},
		{http.MethodPost, "/api/projects/" + matrixProjectA + "/log-drains"},
		{http.MethodPatch, "/api/projects/" + matrixProjectA + "/log-drains/ld_1"},
		{http.MethodDelete, "/api/projects/" + matrixProjectA + "/log-drains/ld_1"},
		{http.MethodPost, "/internal/log-forwarder/pull"},
		{http.MethodPost, "/internal/log-forwarder/ack"},
	},
	features.AuthOAuth: {
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/auth-providers"},
		{http.MethodPut, "/api/projects/" + matrixProjectA + "/auth-providers/github"},
	},
	features.AuthGuest: {
		{http.MethodPut, "/api/projects/" + matrixProjectA + "/auth-settings/guest-captcha"},
		{http.MethodDelete, "/api/projects/" + matrixProjectA + "/auth-settings/guest-captcha"},
	},
	features.AuthUser: {
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/end-users/7"},
		{http.MethodPatch, "/api/projects/" + matrixProjectA + "/end-users/7"},
		{http.MethodDelete, "/api/projects/" + matrixProjectA + "/end-users/7"},
	},
	features.HostMetrics: {
		{http.MethodGet, "/api/admin/monitoring/host"},
		{http.MethodPut, "/api/admin/monitoring/alert-settings"},
		{http.MethodPost, "/api/admin/monitoring/alert-settings/test"},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/containers/metrics"},
	},
	features.AppStorage: {
		{http.MethodPost, "/storage/v1/" + matrixProjectA + "/buckets/files/upload-url"},
		{http.MethodGet, "/storage/v1/" + matrixProjectA + "/buckets/files/objects"},
		{http.MethodPut, "/api/projects/" + matrixProjectA + "/storage/buckets/files/access"},
	},
}

func darkFeatureRouter(t *testing.T, on ...features.Feature) (http.Handler, string) {
	t.Helper()
	cfg := config.AppConfig{DeploymentMode: "selfhosted", AppHostingEnabled: true}
	router, who, _ := buildMatrixWith(t, cfg, func(deps *handlerDeps) {
		deps.features = features.NewStatic(on...)
		// Storage mounts only when the object store is configured.
		deps.storageHandler = handler.NewStorageHandler(nil, nil)
		deps.fnHandler = handler.NewFunctionHandler(nil, nil, nil, nil, nil, "")
	})
	return router, who[callerDeveloper]
}

func probeDark(router http.Handler, probe darkProbe, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(probe.method, probe.path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	// Probed on the internal listener, where every route is mounted.
	custommw.MarkInternalListener(router).ServeHTTP(w, req)
	return w
}

// darkWork is every feature that gates background work or response fields,
// not a route; its test proves nothing changes while it is off.
var darkWork = map[features.Feature]string{
	features.MySQLPITR:    "TestMySQLBinlogShippingStartsOnlyWithItsFeature",
	features.LogRetention: "TestLogPlanSweepStartsOnlyWithItsFeature",
	features.AuthOTP:      "TestAuthOTP_OffHidesTheSwitchEvenWithOAuthOn",
	features.AppGitBuilds: "TestAppBuilds_NothingWiredWhileTheFlagIsOff",
}

func TestDarkRoutes_EveryFeatureIsWired(t *testing.T) {
	for _, feature := range features.All() {
		if len(darkRoutes[feature]) == 0 && darkWork[feature] == "" {
			t.Errorf("feature %s hides no route this test probes", feature)
		}
	}
}

func TestDarkRoutes_AnswerAsUnmountedWhenOff(t *testing.T) {
	router, token := darkFeatureRouter(t)
	for feature, probes := range darkRoutes {
		for _, probe := range probes {
			w := probeDark(router, probe, token)
			if w.Code != http.StatusNotFound || w.Body.String() != unmountedBody {
				t.Errorf("%s off: %s %s answered %d %q", feature, probe.method, probe.path, w.Code, w.Body.String())
			}
		}
	}
}

func TestDarkRoutes_AreServedWhenOn(t *testing.T) {
	for feature, probes := range darkRoutes {
		router, token := darkFeatureRouter(t, feature)
		for _, probe := range probes {
			if w := probeDark(router, probe, token); w.Body.String() == unmountedBody {
				t.Errorf("%s on: %s %s is still unmounted", feature, probe.method, probe.path)
			}
		}
	}
}

// One feature's switch never lights another's routes.
func TestDarkRoutes_FollowOnlyTheirOwnFeature(t *testing.T) {
	for feature, probes := range darkRoutes {
		for _, other := range features.All() {
			if other == feature {
				continue
			}
			router, token := darkFeatureRouter(t, other)
			for _, probe := range probes {
				if w := probeDark(router, probe, token); w.Body.String() != unmountedBody {
					t.Errorf("only %s on: %s %s answered %d", other, probe.method, probe.path, w.Code)
				}
			}
		}
	}
}

func TestConfig_ReportsEveryFeature(t *testing.T) {
	for _, on := range [][]features.Feature{nil, {features.MCP}, features.All()} {
		router, _ := darkFeatureRouter(t, on...)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var body struct {
			Features map[string]bool `json:"features"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode /api/config: %v", err)
		}
		want := features.Snapshot(t.Context(), features.NewStatic(on...))
		if len(body.Features) != len(want) {
			t.Fatalf("on %v: /api/config features %v, want %v", on, body.Features, want)
		}
		for name, enabled := range want {
			if body.Features[name] != enabled {
				t.Errorf("on %v: /api/config says %s=%v", on, name, body.Features[name])
			}
		}
	}
}

func TestImageWatcher_RunsOnlyWithThePipelineOn(t *testing.T) {
	hosting := config.AppConfig{AppHostingEnabled: true}
	cases := []struct {
		cfg   config.AppConfig
		flags features.Flags
		want  bool
	}{
		{hosting, features.NewStatic(), false},
		{hosting, nil, false},
		{hosting, features.NewStatic(features.MCP), false},
		{hosting, features.NewStatic(features.Pipeline), true},
		{config.AppConfig{}, features.NewStatic(features.Pipeline), false},
	}
	for _, tc := range cases {
		if got := imageWatcherWanted(t.Context(), tc.cfg, tc.flags); got != tc.want {
			t.Errorf("hosting=%v flags=%v: watcher wanted=%v, want %v", tc.cfg.AppHostingEnabled, tc.flags, got, tc.want)
		}
	}
}

// EXC-662: either sign-in flag makes the redirect list an Admin's to write.
func TestAuthSettingsWritesNeedAnAdminWhileAnySignInFlagIsOn(t *testing.T) {
	put := darkProbe{http.MethodPut, "/api/projects/" + matrixProjectA + "/auth-settings"}
	for _, on := range [][]features.Feature{{features.AuthOAuth}, {features.AuthOTP}} {
		router, developer := darkFeatureRouter(t, on...)
		if w := probeDark(router, put, developer); w.Code != http.StatusForbidden {
			t.Errorf("%v on: a Developer's write answered %d", on, w.Code)
		}
	}
	router, developer := darkFeatureRouter(t)
	if w := probeDark(router, put, developer); w.Code == http.StatusForbidden {
		t.Errorf("both off: a Developer's write was refused (%s)", w.Body.String())
	}
}
