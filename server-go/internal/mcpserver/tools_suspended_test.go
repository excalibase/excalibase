package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-845: every tool reaches a project through the router, so a suspended
// owner's project (which reads as unknown at /info) is refused wherever the
// router refuses. The two tools that also send traffic to the data plane
// themselves ask /info first; they must send nothing once it refuses.
var directTrafficTools = map[string]map[string]any{
	"test_api_request": {"project_id": testProjectA, "publishable_key": testPublishableKey, "path": "todos"},
	"test_auth_flow":   flowArgs("login"),
}

func TestDirectTrafficToolsSendNothingForAProjectThatReadsAsUnknown(t *testing.T) {
	for name, args := range directTrafficTools {
		t.Run(name, func(t *testing.T) {
			var sent atomic.Int32
			plane := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sent.Add(1) }))
			t.Cleanup(plane.Close)
			routes := newFakeRoutes()
			routes.on(http.MethodGet, projectsA+"/info/", http.StatusNotFound, `{"error":"project not found"}`)
			settings := testSettings
			settings.DataPlaneURL = plane.URL
			cs := sessionWith(t, routes, &recordingAudit{}, writeCaller(), settings)
			if res := callTool(t, cs, name, args); !res.IsError {
				t.Fatalf("%s answered for a project the router refuses", name)
			}
			if sent.Load() != 0 {
				t.Fatalf("%s sent %d requests to the data plane", name, sent.Load())
			}
		})
	}
}

// A write tool whose case only reads through the router does its own traffic;
// it must be named in directTrafficTools so the test above covers it.
func TestEveryWriteToolEitherWritesThroughTheRouterOrIsDeclaredDirect(t *testing.T) {
	for _, tc := range routeCases() {
		if _, direct := directTrafficTools[tc.tool]; direct || !isWriteTool(tc.tool) {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			if !writesThroughTheRouter(t, tc) {
				t.Errorf("%s changes things without a write through the router: add it to directTrafficTools", tc.tool)
			}
		})
	}
}

func writesThroughTheRouter(t *testing.T, tc routeCase) bool {
	t.Helper()
	routes := newFakeRoutes()
	if tc.setup != nil {
		tc.setup(routes)
	}
	settings := testSettings
	if tc.plane != nil {
		plane := httptest.NewServer(tc.plane)
		t.Cleanup(plane.Close)
		settings.DataPlaneURL = plane.URL
	}
	cs := sessionWith(t, routes, &recordingAudit{}, writeCaller(), withFeatures(settings, tc))
	callTool(t, cs, tc.tool, tc.args)
	for _, call := range routes.calls {
		if call.Method != http.MethodGet {
			return true
		}
	}
	return false
}

func isWriteTool(name string) bool {
	for _, e := range catalogue() {
		if e.name == name && e.access == writeTool {
			return true
		}
	}
	return false
}

func withFeatures(settings Settings, tc routeCase) Settings {
	if tc.features != nil {
		settings.Features = features.NewStatic(tc.features...)
	}
	return settings
}
