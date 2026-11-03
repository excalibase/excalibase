package routepolicy

import (
	"net/http"
	"testing"
)

// EXC-668: any member reads where the project stands; only an Admin starts
// the one-way upgrade, and never on a MySQL project.
func TestMajorUpgradeRows(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	read, ok := index[Key{Method: http.MethodGet, Pattern: "/api/provision/{projectId}/major-upgrade/"}]
	if !ok || read.MinRole != roleViewer || !read.Database || !read.MySQL || read.Capability != "" {
		t.Errorf("GET: %+v", read)
	}
	start, ok := index[Key{Method: http.MethodPost, Pattern: "/api/provision/{projectId}/major-upgrade/"}]
	if !ok || start.MinRole != roleAdmin || !start.Database || start.MySQL || start.MySQLRefusal == "" || start.Capability != "" {
		t.Errorf("POST: %+v", start)
	}
}
