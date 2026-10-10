package main

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/routepolicy"
)

const mysqlRefusal = `"not available for MySQL projects"`

// ADR 0041, EXC-583: a database route answers a MySQL project only when its
// row says it serves MySQL; every other one is refused with 409 before a
// handler can run Postgres against it. Walking the table is what catches a
// route added later without saying whether it serves MySQL.
func TestDatabaseRoutesRefuseAMySQLProjectUnlessTheyServeIt(t *testing.T) {
	harness, principals := policyRouterWith(t, func(inst *domain.DatabaseInstance) { inst.DBType = domain.MySQL })
	owner := principalNamed(t, principals, "orgOwner")
	index, err := routepolicy.Index()
	if err != nil {
		t.Fatalf("route policy table: %v", err)
	}
	served := 0
	for key, row := range index {
		if !row.Database || row.Param != routepolicy.ParamProject || row.Owner != routepolicy.OwnerProjectAccess {
			continue
		}
		t.Run(key.String(), func(t *testing.T) {
			code, body := harness.request(key.Method, concreteRequestPath(key.Pattern), owner.token)
			refused := code == 409 && strings.Contains(body, mysqlRefusal)
			if row.MySQL && refused {
				t.Fatalf("a route the table says serves MySQL refused it: %d %s", code, strings.TrimSpace(body))
			}
			if !row.MySQL && !refused {
				t.Fatalf("a Postgres-only route answered %d (%s); want 409 %s", code, strings.TrimSpace(body), mysqlRefusal)
			}
		})
		if row.MySQL {
			served++
		}
	}
	if served == 0 {
		t.Error("no database route serves MySQL: pause, resume, upgrade and the plan change must")
	}
}

// What MySQL projects are served on EXC-583: their lifecycle.
func TestMySQLLifecycleRoutesServeMySQL(t *testing.T) {
	index, err := routepolicy.Index()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []routepolicy.Key{
		{Method: "POST", Pattern: "/api/provision/{projectId}/pause"},
		{Method: "POST", Pattern: "/api/provision/{projectId}/resume"},
		{Method: "POST", Pattern: "/api/provision/{projectId}/upgrade"},
		{Method: "POST", Pattern: "/api/provision/{projectId}/tier"},
	} {
		if row, ok := index[key]; !ok || !row.MySQL {
			t.Errorf("%s does not serve MySQL projects", key)
		}
	}
}
