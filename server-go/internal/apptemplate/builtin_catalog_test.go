package apptemplate

import (
	"strings"
	"testing"
)

var catalogIDs = []string{"valkey", "mongodb", "rabbitmq"}

func TestBuiltinCatalogIsMarkedApartFromTheStarters(t *testing.T) {
	catalog, err := Builtins()
	if err != nil {
		t.Fatalf("builtins: %v", err)
	}
	for _, id := range catalogIDs {
		tpl, ok := catalog.Get(id)
		if !ok {
			t.Errorf("catalog template %q is missing", id)
			continue
		}
		if !tpl.Catalog {
			t.Errorf("%s: a catalog template is marked as one", id)
		}
	}
	for _, id := range []string{"redis", "web-redis", "web-postgres", "storefront-demo"} {
		if tpl, ok := catalog.Get(id); !ok || tpl.Catalog || tpl.Category != "starter" {
			t.Errorf("%s: a starter is not a catalog template: %+v", id, tpl)
		}
	}
}

// Every built-in says what it is and under which licence its images come.
func TestBuiltinsNameCategoryAndLicense(t *testing.T) {
	catalog, err := Builtins()
	if err != nil {
		t.Fatalf("builtins: %v", err)
	}
	for _, tpl := range catalog.List() {
		if tpl.Category == "" || tpl.License == "" {
			t.Errorf("%s: category %q, license %q", tpl.ID, tpl.Category, tpl.License)
		}
	}
}

// publicReaches lists every variable of tpl that reads a public app's host or port.
func publicReaches(t *testing.T, tpl *Template) []string {
	t.Helper()
	internal := map[string]bool{}
	for _, app := range tpl.Apps {
		internal[app.Name] = app.Internal
	}
	var found []string
	for _, app := range tpl.Apps {
		for _, v := range app.Env {
			parts, err := parseValue(v.Value)
			if err != nil {
				t.Fatalf("%s/%s: %v", tpl.ID, v.Name, err)
			}
			for _, p := range parts {
				if p.expr != nil && (p.expr.kind == exprAppHost || p.expr.kind == exprAppPort) && !internal[p.expr.app] {
					found = append(found, app.Name+"/"+v.Name)
				}
			}
		}
	}
	return found
}

// A built-in reaches another app only by an internal service's host and port:
// a public app's port is 80 on Kubernetes and its own port on a single host.
func TestBuiltinsReachOnlyInternalServices(t *testing.T) {
	catalog, err := Builtins()
	if err != nil {
		t.Fatalf("builtins: %v", err)
	}
	for _, tpl := range catalog.List() {
		if found := publicReaches(t, tpl); len(found) > 0 {
			t.Errorf("%s reaches a public app from %v", tpl.ID, found)
		}
	}
}

// Each app either runs in Free's 256Mi or says what it needs.
func TestCatalogStatesMemoryAboveFree(t *testing.T) {
	catalog, err := Builtins()
	if err != nil {
		t.Fatalf("builtins: %v", err)
	}
	needs := map[string]bool{"mongodb/mongodb": true, "rabbitmq/rabbitmq": true}
	for _, tpl := range catalog.List() {
		for _, app := range tpl.Apps {
			bytes, err := app.MinMemoryBytes()
			if err != nil {
				t.Fatalf("%s/%s: %v", tpl.ID, app.Name, err)
			}
			if (bytes > 256<<20) != needs[tpl.ID+"/"+app.Name] {
				t.Errorf("%s/%s: minMemory %q", tpl.ID, app.Name, app.MinMemory)
			}
		}
	}
}

// Generated passwords never appear in a template; each service gets its own.
func TestCatalogGeneratesItsSecrets(t *testing.T) {
	catalog, err := Builtins()
	if err != nil {
		t.Fatalf("builtins: %v", err)
	}
	for _, id := range catalogIDs {
		tpl, _ := catalog.Get(id)
		if tpl == nil || !tpl.Facts().GeneratesSecrets {
			t.Errorf("%s generates its credentials", id)
		}
		if tpl != nil && strings.Contains(strings.ToLower(tpl.Source), "password: ") {
			t.Errorf("%s: a literal password in the source", id)
		}
	}
}
