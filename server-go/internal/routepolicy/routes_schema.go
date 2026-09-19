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
		Owner: OwnerProjectAccess, MinRole: roleViewer,
	}
}

// schemaWrite is DDL, /query or a row write under /api/schema/{projectId}:
// it changes what the tenant's database serves, so it sits on the authoring
// rung. RequireProjectRoleForWrites enforces the split by method.
func schemaWrite(method, pattern string) Row {
	return Row{
		Methods: []string{method}, Pattern: "/api/schema/{projectId}" + pattern,
		Auth: AuthSession, Param: ParamProject,
		Owner: OwnerProjectAccess, MinRole: roleDeveloper,
	}
}

// schemaRows is the studio's schema browser and editor.
var schemaRows = append(schemaReadRows(), schemaWriteRows()...)

func schemaReadRows() []Row {
	return []Row{
		schemaRead("/advisors/performance"),
		schemaRead("/advisors/security"),
		schemaRead("/connection-test"),
		schemaRead("/extensions"),
		schemaRead("/functions"),
		schemaRead("/policies"),
		schemaRead("/relationships"),
		schemaRead("/roles"),
		schemaRead("/tables"),
		schemaRead("/tables/{tableName}/columns"),
		schemaRead("/tables/{tableName}/indexes"),
		schemaRead("/tables/{tableName}/rows"),
		schemaRead("/triggers"),
		schemaRead("/types"),
	}
}

func schemaWriteRows() []Row {
	return []Row{
		schemaWrite(http.MethodPost, "/ddl"),
		schemaWrite(http.MethodPost, "/query"),
		schemaWrite(http.MethodPost, "/extensions"),
		schemaWrite(http.MethodPost, "/functions"),
		schemaWrite(http.MethodPost, "/indexes"),
		schemaWrite(http.MethodPost, "/policies"),
		schemaWrite(http.MethodPost, "/roles"),
		schemaWrite(http.MethodPost, "/tables"),
		schemaWrite(http.MethodPost, "/triggers"),
		schemaWrite(http.MethodPost, "/tables/{tableName}/columns"),
		schemaWrite(http.MethodPost, "/tables/{tableName}/rows"),
		schemaWrite(http.MethodPatch, "/tables/{tableName}"),
		schemaWrite(http.MethodPatch, "/tables/{tableName}/columns/{columnName}"),
		schemaWrite(http.MethodPatch, "/tables/{tableName}/rows"),
		schemaWrite(http.MethodDelete, "/extensions/{extName}"),
		schemaWrite(http.MethodDelete, "/functions/{funcName}"),
		schemaWrite(http.MethodDelete, "/indexes/{indexName}"),
		schemaWrite(http.MethodDelete, "/policies/{policyName}"),
		schemaWrite(http.MethodDelete, "/roles/{roleName}"),
		schemaWrite(http.MethodDelete, "/triggers/{triggerName}"),
		schemaWrite(http.MethodDelete, "/tables/{tableName}"),
		schemaWrite(http.MethodDelete, "/tables/{tableName}/columns/{columnName}"),
		schemaWrite(http.MethodDelete, "/tables/{tableName}/rows"),
	}
}
