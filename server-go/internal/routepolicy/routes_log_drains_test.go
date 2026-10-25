package routepolicy

import (
	"net/http"
	"testing"
)

// EXC-658: only a project's Admins see or change its drains, and only the
// forwarder's token reaches the batches with their keys.
func TestLogDrainRows(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	for _, key := range []Key{
		{Method: http.MethodGet, Pattern: "/api/projects/{projectId}/log-drains/"},
		{Method: http.MethodPost, Pattern: "/api/projects/{projectId}/log-drains/"},
		{Method: http.MethodPatch, Pattern: "/api/projects/{projectId}/log-drains/{drainId}"},
		{Method: http.MethodDelete, Pattern: "/api/projects/{projectId}/log-drains/{drainId}"},
	} {
		row, ok := index[key]
		if !ok || row.MinRole != roleAdmin || row.Param != ParamProject || row.Auth != AuthSession || row.Capability != "" {
			t.Errorf("%s: %+v", key, row)
		}
	}
	for _, key := range []Key{
		{Method: http.MethodPost, Pattern: "/internal/log-forwarder/pull"},
		{Method: http.MethodPost, Pattern: "/internal/log-forwarder/ack"},
	} {
		row, ok := index[key]
		if !ok || row.Auth != AuthForwarderToken || row.Capability != "" {
			t.Errorf("%s: %+v", key, row)
		}
		for _, p := range []Principal{
			{Name: "platformAdmin", PlatformAdmin: true},
			{Name: "orgOwner", OrgRole: "owner"},
		} {
			if got := Expect(row, http.MethodPost, p); got != Deny401 {
				t.Errorf("%s for %s: %s", key, p.Name, got)
			}
		}
	}
}
