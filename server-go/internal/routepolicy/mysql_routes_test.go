package routepolicy

import (
	"strings"
	"testing"
)

// EXC-619: Studio's table editor, SQL runner and the permissions grid serve a
// MySQL project; the Postgres-only pages do not, and say why.
func TestSchemaRoutesThatServeMySQL(t *testing.T) {
	for _, request := range [][2]string{
		{"GET", "/api/schema/p1/tables"},
		{"POST", "/api/schema/p1/tables"},
		{"DELETE", "/api/schema/p1/tables/orders"},
		{"GET", "/api/schema/p1/tables/orders/columns"},
		{"POST", "/api/schema/p1/tables/orders/columns"},
		{"DELETE", "/api/schema/p1/tables/orders/columns/note"},
		{"GET", "/api/schema/p1/tables/orders/indexes"},
		{"GET", "/api/schema/p1/tables/orders/checks"},
		{"GET", "/api/schema/p1/tables/orders/rows"},
		{"POST", "/api/schema/p1/tables/orders/rows"},
		{"PATCH", "/api/schema/p1/tables/orders/rows"},
		{"DELETE", "/api/schema/p1/tables/orders/rows"},
		{"GET", "/api/schema/p1/relationships"},
		{"POST", "/api/schema/p1/indexes"},
		{"DELETE", "/api/schema/p1/indexes/orders_total"},
		{"POST", "/api/schema/p1/query"},
		{"GET", "/api/schema/p1/query"},
		{"POST", "/api/schema/p1/ddl"},
		{"GET", "/api/schema/p1/connection-test"},
		{"PUT", "/api/provision/p1/permissions/tables/app.orders/roles/anon/select"},
		{"DELETE", "/api/provision/p1/permissions/tables/app.orders/roles/anon/select"},
	} {
		row, ok := Lookup(request[0], request[1])
		if !ok || !row.MySQL {
			t.Errorf("%s %s: found %v, MySQL %v", request[0], request[1], ok, row.MySQL)
		}
	}
}

func TestPostgresOnlyRoutesNameTheirReasonForMySQL(t *testing.T) {
	for request, reason := range map[[2]string]string{
		{"GET", "/api/schema/p1/extensions"}:                        "extensions",
		{"GET", "/api/schema/p1/policies"}:                          "row-level security",
		{"GET", "/api/schema/p1/types"}:                             "user-defined types",
		{"GET", "/api/schema/p1/advisors/security"}:                 "statistics",
		{"GET", "/api/schema/p1/functions"}:                         "scalars",
		{"POST", "/api/provision/p1/tracked-functions/"}:            "scalars",
		{"PUT", "/api/provision/p1/function-permissions/f/roles/r"}: "scalars",
		{"POST", "/api/provision/p1/migrations/"}:                   "not transactional",
		{"PUT", "/api/projects/p1/realtime/tables/app/orders"}:      "binlog",
	} {
		row, ok := Lookup(request[0], request[1])
		if !ok || row.MySQL {
			t.Errorf("%s %s: found %v, MySQL %v", request[0], request[1], ok, row.MySQL)
			continue
		}
		if !strings.Contains(row.MySQLRefusal, reason) {
			t.Errorf("%s %s: refusal %q lacks %q", request[0], request[1], row.MySQLRefusal, reason)
		}
	}
}

// A reason only means something on a database route MySQL is refused.
func TestARefusalReasonSitsOnlyOnARefusedDatabaseRoute(t *testing.T) {
	for _, row := range Table() {
		if row.MySQLRefusal != "" && (row.MySQL || !row.Database) {
			t.Errorf("%s %v: reason on a route that is not refused", row.Pattern, row.Methods)
		}
	}
}
