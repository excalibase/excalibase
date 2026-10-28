package mcpserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

const allowListA = projectsA + "/db-endpoint/allowed-sources/"

func dbAllowListRouteCases() []routeCase {
	on := []features.Feature{features.DBAllowList}
	return []routeCase{
		{
			name: "get_db_allowed_sources", tool: "get_db_allowed_sources", args: map[string]any{"project_id": testProjectA},
			features: on,
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, allowListA, 200, `{"projectId":"proj-a","access":"listed","sources":["203.0.113.0/24"],"max":20,"publicEnabled":true,"canChange":true,"callerAddress":"198.51.100.23"}`)
			},
			expect: []string{"GET " + allowListA},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if out["access"] != "listed" || out["callerAddress"] != nil {
					t.Fatalf("out = %v", out)
				}
			},
		},
		{
			name: "set_db_allowed_sources", tool: "set_db_allowed_sources",
			args:     map[string]any{"project_id": testProjectA, "sources": []string{"198.51.100.7"}},
			features: on,
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPut, allowListA, 200, `{"projectId":"proj-a","access":"listed","sources":["198.51.100.7/32"],"max":20,"publicEnabled":false}`)
			},
			expect: []string{"PUT " + allowListA},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				if body := calls[len(calls)-1].Body; !strings.Contains(body, `"sources":["198.51.100.7"]`) || strings.Contains(body, "allowAny") {
					t.Fatalf("body = %s", body)
				}
				if note, _ := out["note"].(string); !strings.Contains(note, "Studio") {
					t.Fatalf("note = %q", note)
				}
			},
		},
	}
}

func TestSetDBAllowedSourcesRefusesEveryAddressAndAnEmptyList(t *testing.T) {
	for _, sources := range [][]string{nil, {"0.0.0.0/0"}, {"::/0"}, {"*"}, {"10.0.0.0/8"}, {"198.51.100.7", "203.0.0.0/16"}, {"2001:db8::/32"}} {
		if _, err := mcpAllowListSources(sources); err == nil {
			t.Errorf("%v accepted", sources)
		}
	}
}

func TestSetDBAllowedSourcesTakesNarrowEntries(t *testing.T) {
	if _, err := mcpAllowListSources([]string{"198.51.100.7", "203.0.113.0/24", "2001:db8:1::/48"}); err != nil {
		t.Fatalf("narrow entries refused: %v", err)
	}
}
