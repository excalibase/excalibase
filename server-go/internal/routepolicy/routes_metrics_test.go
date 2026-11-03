package routepolicy

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-657: a metrics key reads its project's metrics endpoint, and no other
// row in the whole table admits it.
func TestOnlyTheMetricsEndpointAdmitsAMetricsKey(t *testing.T) {
	want := map[Key]bool{{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/metrics/"}: true}
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	for key, row := range index {
		if row.MetricsKey != want[key] {
			t.Errorf("%s: MetricsKey = %v, want %v", key, row.MetricsKey, want[key])
		}
	}
	metricsKey := Principal{Name: "metricsKey", OrgRole: domain.OrgRoleAdmin, ReadOnly: true, Restricted: true, MetricsKey: true}
	for key, row := range index {
		got := Expect(row, key.Method, metricsKey)
		switch {
		case key.Method == http.MethodOptions:
		case want[key] && got != Allow:
			t.Errorf("%s: %s, want the metrics key allowed", key, got)
		case !want[key] && got != Deny403:
			t.Errorf("%s: %s, want the metrics key refused 403", key, got)
		}
	}
}

// Making and revoking keys is an Admin's; any member reads the endpoint.
func TestMetricsKeyRowsAreAdminOnly(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	for _, key := range []Key{
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/metrics-keys/"},
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/metrics-keys/"},
		{Method: http.MethodDelete, Pattern: "/api/projects/{projectId}/metrics-keys/{keyId}"},
	} {
		if row, ok := index[key]; !ok || row.MinRole != roleAdmin || row.Param != ParamProject {
			t.Errorf("%s: %+v", key, row)
		}
	}
	if row := index[Key{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/metrics/"}]; row.MinRole != roleViewer {
		t.Errorf("endpoint rung: %+v", row)
	}
}
