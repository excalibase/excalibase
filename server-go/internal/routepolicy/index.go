package routepolicy

import (
	"fmt"
	"net/http"
)

// MethodAll stands for every method chi registers when a route is mounted
// with Handle/HandleFunc rather than a per-method helper.
const MethodAll = "*"

// allMethods is what chi registers for a catch-all mount, in the order the
// router reports them.
var allMethods = []string{
	http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
	http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut,
	http.MethodTrace,
}

// Key identifies one mounted route: its method and its chi pattern.
type Key struct {
	Method  string
	Pattern string
}

func (k Key) String() string { return k.Method + " " + k.Pattern }

// Index expands the table into one entry per method + pattern. It fails when
// two rows claim the same route, because the duplicate would silently shadow
// whichever contract the reader did not look at.
func Index() (map[Key]Row, error) { return indexRows(Table()) }

// indexRows is Index over an explicit row set, so the duplicate guard can be
// exercised without corrupting the real table.
func indexRows(rows []Row) (map[Key]Row, error) {
	out := make(map[Key]Row, len(rows)*2)
	for _, row := range rows {
		for _, method := range row.expandMethods() {
			key := Key{Method: method, Pattern: row.Pattern}
			if _, taken := out[key]; taken {
				return nil, fmt.Errorf("route policy declares %s twice", key)
			}
			out[key] = row
		}
	}
	return out, nil
}

// expandMethods resolves MethodAll into the concrete method set.
func (r Row) expandMethods() []string {
	for _, method := range r.Methods {
		if method == MethodAll {
			return allMethods
		}
	}
	return r.Methods
}
