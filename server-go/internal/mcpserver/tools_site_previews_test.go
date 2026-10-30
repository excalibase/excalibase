package mcpserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

func sitePreviewRouteCases() []routeCase {
	on := []features.Feature{features.SitePreviews}
	return []routeCase{
		{
			name: "list_site_previews", tool: "list_site_previews", args: map[string]any{"project_id": testProjectA, "site_id": "s1"},
			features: on,
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/sites/s1/previews", 200,
					`{"settings":{"mode":"branches"},"previews":[{"id":"p1","hostname":"shop--abc.excalibase.app","status":"READY"}],"used":1,"max":2}`)
			},
			expect: []string{"GET " + projectsA + "/sites/s1/previews"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if previews, _ := out["previews"].([]any); len(previews) != 1 || out["max"] == nil {
					t.Fatalf("out = %v", out)
				}
			},
		},
		{
			name: "set_site_preview_mode", tool: "set_site_preview_mode", args: map[string]any{"project_id": testProjectA, "site_id": "s1", "mode": "pull-requests"},
			features: on,
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPut, projectsA+"/sites/s1/previews/mode", 200, `{"settings":{"mode":"pull-requests"},"previews":[]}`)
			},
			expect: []string{"PUT " + projectsA + "/sites/s1/previews/mode"},
			check: func(t *testing.T, calls []recordedCall, _ map[string]any) {
				if !strings.Contains(calls[0].Body, `"mode":"pull-requests"`) {
					t.Errorf("body = %s", calls[0].Body)
				}
			},
		},
		{
			name: "remove_site_preview", tool: "remove_site_preview", args: map[string]any{"project_id": testProjectA, "site_id": "s1", "preview_id": "p1"},
			features: on,
			setup: func(f *fakeRoutes) {
				f.on(http.MethodDelete, projectsA+"/sites/s1/previews/p1", 204, ``)
			},
			expect: []string{"DELETE " + projectsA + "/sites/s1/previews/p1"},
		},
	}
}
