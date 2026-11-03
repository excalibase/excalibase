package routepolicy

import "net/http"

// tusReads and tusWrites split the resumable-upload mounts, which register
// every method on one pattern. HEAD reports an upload's offset; the rest
// create, append to or terminate an upload.
var (
	tusReads  = []string{http.MethodGet, http.MethodHead}
	tusWrites = []string{
		http.MethodConnect, http.MethodDelete, http.MethodOptions,
		http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace,
	}
)

// schemaRead is a browse endpoint under /api/schema/{projectId}: any member
// may look at the tenant's catalogue.
func schemaRead(pattern string) Row {
	return Row{
		Methods: get, Pattern: "/api/schema/{projectId}" + pattern,
		Auth: AuthSession, Param: ParamProject,
		Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true,
		Discloses: NoSecret,
	}
}

// schemaWrite is DDL, /query or a row write under /api/schema/{projectId}:
// it changes what the tenant's database serves, so it sits on the authoring
// rung. RequireProjectRoleForWrites enforces the split by method.
func schemaWrite(method, pattern string) Row {
	return Row{
		Methods: []string{method}, Pattern: "/api/schema/{projectId}" + pattern,
		Auth: AuthSession, Param: ParamProject,
		Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true,
	}
}

// schemaRows is the studio's schema browser and editor.
var schemaRows = append(schemaReadRows(), schemaWriteRows()...)

func schemaReadRows() []Row {
	return []Row{
		refusedOnMySQL(schemaRead("/advisors/performance"), mysqlNoAdvisors),
		refusedOnMySQL(schemaRead("/advisors/security"), mysqlNoAdvisors),
		mysqlCapable(schemaRead("/connection-test")),
		refusedOnMySQL(schemaRead("/extensions"), mysqlNoExtensions),
		refusedOnMySQL(schemaRead("/functions"), MySQLNoTrackedFunctions),
		refusedOnMySQL(schemaRead("/policies"), mysqlNoPolicies),
		{
			Methods: get, Pattern: "/api/schema/{projectId}/query",
			Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true, Discloses: NoSecret,
			Note: "read-only SQL in a read-only transaction (EXC-544): a read, yet on the rung that may run SQL at all", MySQL: true,
		},
		mysqlCapable(schemaRead("/relationships")),
		refusedOnMySQL(schemaRead("/roles"), mysqlNoRoles),
		mysqlCapable(schemaRead("/tables")),
		mysqlCapable(schemaRead("/tables/{tableName}/columns")),
		mysqlCapable(schemaRead("/tables/{tableName}/indexes")),
		mysqlCapable(schemaRead("/tables/{tableName}/checks")),
		mysqlCapable(schemaRead(tableRowsPattern)),
		refusedOnMySQL(schemaRead("/triggers"), mysqlNoTriggers),
		refusedOnMySQL(schemaRead("/types"), mysqlNoTypes),
	}
}

func schemaWriteRows() []Row {
	return []Row{
		mysqlCapable(schemaWrite(http.MethodPost, "/ddl")),
		// Table import (EXC-368): the preview writes nothing but parses an
		// upload or fetches a sheet, so it sits with the writes.
		schemaWrite(http.MethodPost, "/import"),
		schemaWrite(http.MethodPost, "/import/preview"),
		mysqlCapable(schemaWrite(http.MethodPost, "/query")),
		refusedOnMySQL(schemaWrite(http.MethodPost, "/extensions"), mysqlNoExtensions),
		refusedOnMySQL(schemaWrite(http.MethodPost, "/functions"), MySQLNoTrackedFunctions),
		mysqlCapable(schemaWrite(http.MethodPost, "/indexes")),
		refusedOnMySQL(schemaWrite(http.MethodPost, "/policies"), mysqlNoPolicies),
		refusedOnMySQL(schemaWrite(http.MethodPost, "/roles"), mysqlNoRoles),
		mysqlCapable(schemaWrite(http.MethodPost, "/tables")),
		refusedOnMySQL(schemaWrite(http.MethodPost, "/triggers"), mysqlNoTriggers),
		mysqlCapable(schemaWrite(http.MethodPost, "/tables/{tableName}/columns")),
		mysqlCapable(schemaWrite(http.MethodPost, tableRowsPattern)),
		schemaWrite(http.MethodPatch, "/tables/{tableName}"),
		schemaWrite(http.MethodPatch, "/tables/{tableName}/columns/{columnName}"),
		mysqlCapable(schemaWrite(http.MethodPatch, tableRowsPattern)),
		refusedOnMySQL(schemaWrite(http.MethodDelete, "/extensions/{extName}"), mysqlNoExtensions),
		refusedOnMySQL(schemaWrite(http.MethodDelete, "/functions/{funcName}"), MySQLNoTrackedFunctions),
		mysqlCapable(schemaWrite(http.MethodDelete, "/indexes/{indexName}")),
		refusedOnMySQL(schemaWrite(http.MethodDelete, "/policies/{policyName}"), mysqlNoPolicies),
		refusedOnMySQL(schemaWrite(http.MethodDelete, "/roles/{roleName}"), mysqlNoRoles),
		refusedOnMySQL(schemaWrite(http.MethodDelete, "/triggers/{triggerName}"), mysqlNoTriggers),
		mysqlCapable(schemaWrite(http.MethodDelete, "/tables/{tableName}")),
		mysqlCapable(schemaWrite(http.MethodDelete, "/tables/{tableName}/columns/{columnName}")),
		mysqlCapable(schemaWrite(http.MethodDelete, tableRowsPattern)),
	}
}

const tableRowsPattern = "/tables/{tableName}/rows"

// mysqlCapable marks a schema route the MySQL catalogue serves (EXC-619).
func mysqlCapable(row Row) Row {
	row.MySQL = true
	return row
}

// refusedOnMySQL names why a MySQL project cannot use the route.
func refusedOnMySQL(row Row, reason string) Row {
	row.MySQLRefusal = reason
	return row
}

// Why the Postgres-only database routes refuse a MySQL project (ADR 0041
// decision 9).
const (
	mysqlNoExtensions = "MySQL has no extensions; its features ship built in"
	mysqlNoPolicies   = "CREATE POLICY is Postgres row-level security; on MySQL, API access is set in each table's permissions"
	mysqlNoTypes      = "MySQL has no user-defined types; ENUM and SET are column types"
	mysqlNoAdvisors   = "the advisors read Postgres statistics catalogs"
	mysqlNoRoles      = "MySQL users are created and rotated by the platform"
	mysqlNoTriggers   = "Studio's trigger editor builds Postgres trigger functions; create MySQL triggers with SQL"
	// MySQLNoTrackedFunctions is also MCP's answer to track_db_function.
	MySQLNoTrackedFunctions = "tracked functions are not offered on MySQL: MySQL functions return scalars, and API permissions apply to rows"
	// MySQLNoMigrations is also MCP's answer to apply_migration.
	MySQLNoMigrations = "migration history is Postgres-only for now; run a MySQL migration with the SQL runner, " +
		"and note MySQL DDL is not transactional, so a failed migration is not rolled back"
	mysqlNoRealtime = "realtime on MySQL reads the binlog through the watcher, which is not served yet"
)
