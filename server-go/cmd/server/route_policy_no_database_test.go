package main

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/routepolicy"
)

const noDatabaseRefusal = `"project has no database"`

// EXC-426: every route the table marks as working on the project's database
// answers 409 "project has no database" for a project created without one,
// before any handler can reach for a cluster or credential that does not
// exist. Every other project route is reachable. Walking the table rather
// than a hand-picked list is what catches a database route mounted without
// the guard.
func TestEveryDatabaseRouteRefusesAProjectWithoutOne(t *testing.T) {
	harness, principals := policyRouterWith(t, func(inst *domain.DatabaseInstance) { inst.NoDatabase = true })
	owner := principalNamed(t, principals, "orgOwner")
	index, err := routepolicy.Index()
	if err != nil {
		t.Fatalf("route policy table: %v", err)
	}
	for key, row := range index {
		if row.Param != routepolicy.ParamProject || row.Owner != routepolicy.OwnerProjectAccess {
			continue
		}
		t.Run(key.String(), func(t *testing.T) {
			code, body := harness.request(key.Method, concreteRequestPath(key.Pattern), owner.token)
			refused := code == 409 && strings.Contains(body, noDatabaseRefusal)
			if row.Database && !refused {
				t.Fatalf("a database route answered %d (%s); want 409 %s", code, strings.TrimSpace(body), noDatabaseRefusal)
			}
			if !row.Database && strings.Contains(body, noDatabaseRefusal) {
				t.Fatalf("a route the table says needs no database refused: %d %s", code, strings.TrimSpace(body))
			}
		})
	}
}

// The same routes serve a project that has its database.
func TestDatabaseRoutesServeAProjectWithOne(t *testing.T) {
	harness, principals := policyRouter(t)
	owner := principalNamed(t, principals, "orgOwner")
	index, err := routepolicy.Index()
	if err != nil {
		t.Fatalf("route policy table: %v", err)
	}
	for key, row := range index {
		if !row.Database {
			continue
		}
		code, body := harness.request(key.Method, concreteRequestPath(key.Pattern), owner.token)
		if strings.Contains(body, noDatabaseRefusal) {
			t.Errorf("%s refused a project that has its database: %d", key, code)
		}
	}
}
