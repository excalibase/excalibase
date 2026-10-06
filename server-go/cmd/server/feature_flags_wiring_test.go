package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-554: what shipped dark answers exactly as a route that was never
// mounted, and answers normally once its feature is on.

// unmountedBody is what chi, and features.Require, answer for a route that is not there.
const unmountedBody = "404 page not found\n"

type darkProbe struct{ method, path string }

// darkRoutes is every route a feature hides. A declared feature with no
// probe here fails TestDarkRoutes_EveryFeatureIsWired.
var darkRoutes = map[features.Feature][]darkProbe{
	features.MCP: {
		{http.MethodPost, "/mcp"},
		{http.MethodGet, "/api/schema/" + matrixProjectA + "/query?sql=select+1"},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/ai-activity/"},
	},
	features.Pipeline: {
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/apps/app-1/deploys/dep-1"},
	},
}

func darkFeatureRouter(t *testing.T, on ...features.Feature) (http.Handler, string) {
	t.Helper()
	cfg := config.AppConfig{DeploymentMode: "selfhosted", AppHostingEnabled: true}
	router, who, _ := buildMatrixWith(t, cfg, func(deps *handlerDeps) {
		deps.features = features.NewStatic(on...)
	})
	return router, who[callerDeveloper]
}

func probeDark(router http.Handler, probe darkProbe, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(probe.method, probe.path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestDarkRoutes_EveryFeatureIsWired(t *testing.T) {
	for _, feature := range features.All() {
		if len(darkRoutes[feature]) == 0 {
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
