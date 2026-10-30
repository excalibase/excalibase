package mcpserver

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// darkTools is every tool offered only while its feature is on.
var darkTools = map[string]features.Feature{
	"get_advisors":          features.Advisors,
	"list_site_previews":    features.SitePreviews,
	"set_site_preview_mode": features.SitePreviews,
	"remove_site_preview":   features.SitePreviews,
}

func advisorRouteCases() []routeCase {
	return []routeCase{{
		name: "get_advisors", tool: "get_advisors", args: map[string]any{"project_id": testProjectA},
		features: []features.Feature{features.Advisors},
		setup: func(f *fakeRoutes) {
			f.on(http.MethodGet, provisionA+"/advisors", 200, `{"engine":"postgres","findings":[{"check":"fk_without_index","category":"performance","level":"warning","object":"public.orders","message":"m","fixHint":"h","fixSql":"CREATE INDEX CONCURRENTLY ON \"public\".\"orders\" (\"customer_id\");"}],"checks":[{"id":"fk_without_index","category":"performance","status":"ran"}]}`)
		},
		expect: []string{"GET " + provisionA + "/advisors"},
		check: func(t *testing.T, _ []recordedCall, out map[string]any) {
			findings, _ := out["findings"].([]any)
			if out["engine"] != "postgres" || len(findings) != 1 {
				t.Fatalf("out = %v", out)
			}
			if note, _ := out["note"].(string); !strings.Contains(note, "never applied") {
				t.Fatalf("note = %q", note)
			}
		},
	}}
}

func listedTools(t *testing.T, settings Settings) []string {
	t.Helper()
	cs := sessionWith(t, newFakeRoutes(), &recordingAudit{}, writeCaller(), settings)
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// EXC-655: a dark tool is absent while its feature is off, and present once on.
func TestDarkToolsFollowTheirFeature(t *testing.T) {
	off := listedTools(t, testSettings)
	for name, feature := range darkTools {
		if slices.Contains(off, name) {
			t.Errorf("%s offered with %s off", name, feature)
		}
		on := testSettings
		on.Features = features.NewStatic(feature)
		if !slices.Contains(listedTools(t, on), name) {
			t.Errorf("%s not offered with %s on", name, feature)
		}
		others := testSettings
		var rest []features.Feature
		for _, other := range features.All() {
			if other != feature {
				rest = append(rest, other)
			}
		}
		others.Features = features.NewStatic(rest...)
		if slices.Contains(listedTools(t, others), name) {
			t.Errorf("%s offered without %s", name, feature)
		}
	}
}

func TestAdvisorsToolIsReadOnly(t *testing.T) {
	settings := testSettings
	settings.Features = features.NewStatic(features.Advisors)
	cs := sessionWith(t, newFakeRoutes(), &recordingAudit{}, readOnlyCaller(), settings)
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "get_advisors" {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatal("get_advisors is not marked read-only")
			}
			return
		}
	}
	t.Fatal("get_advisors not offered to a read-only connection")
}
