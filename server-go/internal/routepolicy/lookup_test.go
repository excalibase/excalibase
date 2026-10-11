package routepolicy

import (
	"strings"
	"testing"
)

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

// chi reports "/db-endpoint/" as "/db-endpoint"; every row, trailing slash
// or not, must still be found by a request to its own path.
func TestLookupFindsEveryRowByItsOwnPath(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatal(err)
	}
	for key := range index {
		path := strings.NewReplacer("{", "", "}", "", "*", "x").Replace(key.Pattern)
		row, ok := Lookup(key.Method, path)
		if !ok || row.Pattern != key.Pattern {
			t.Errorf("%s %s found %v (%q)", key.Method, path, ok, row.Pattern)
		}
	}
}
