package routepolicy

import "testing"

func TestLookupResolvesARequestToItsRow(t *testing.T) {
	row, ok := Lookup("POST", "/api/provision/proj-abc/pause")
	if !ok || row.Pattern != "/api/provision/{projectId}/pause" || !row.MySQL {
		t.Errorf("pause: %+v %v", row, ok)
	}
	row, ok = Lookup("GET", "/api/provision/proj-abc/logs")
	if !ok || row.MySQL || !row.Database {
		t.Errorf("logs: %+v %v", row, ok)
	}
	if _, ok := Lookup("GET", "/api/nowhere"); ok {
		t.Error("an unknown route resolved")
	}
}
