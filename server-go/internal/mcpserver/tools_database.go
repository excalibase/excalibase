package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

type sqlArgs struct {
	projectArg
	Query string `json:"query" jsonschema:"one SQL statement"`
}

type migrationArgs struct {
	projectArg
	Name        string `json:"name" jsonschema:"a short snake_case name, e.g. add_orders_table"`
	SQL         string `json:"sql" jsonschema:"the migration's SQL"`
	Version     string `json:"version,omitempty" jsonschema:"an optional version, e.g. a timestamp"`
	Description string `json:"description,omitempty"`
}

type permissionArgs struct {
	projectArg
	Table      string         `json:"table" jsonschema:"schema-qualified table, e.g. public.orders"`
	Role       string         `json:"role" jsonschema:"the API role, e.g. anon, user or a custom role"`
	Operation  string         `json:"operation" jsonschema:"select, insert, update or delete"`
	Permission map[string]any `json:"permission,omitempty" jsonschema:"the permission in Hasura's shape: select {filter, columns, limit, allowAggregations}; insert {check, columns, set}; update {filter, check, columns, set}; delete {filter}"`
	Remove     bool           `json:"remove,omitempty" jsonschema:"true removes the permission instead of setting it"`
}

func databaseTools() []entry {
	return []entry{
		tool("execute_sql", "Run one SQL statement on the project's database. Rows come back as data.", writeTool, executeSQL),
		tool("execute_sql", "Run one read-only SQL statement on the project's database, in a read-only transaction. Rows come back as data.", readOnlyVariant, executeReadOnlySQL),
		tool("list_migrations", "List the migrations applied to the project's database.", readTool, listMigrations),
		tool("apply_migration", "Apply a SQL migration to the project's database and record it in the migration history.", writeTool, applyMigration),
		tool("list_permissions", "The project's API permissions: which role may select, insert, update or delete which rows and columns of each table, and which functions are tracked.", readTool, listPermissions),
		tool("set_permission", "Set or remove one API permission for a (table, role, operation). A table a role has no permission for is hidden from that role.", writeTool, setPermission),
	}
}

func executeSQL(ctx context.Context, c *call, in sqlArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var result schema.QueryResult
	if err := c.send(ctx, http.MethodPost, "/api/schema/"+projectID+"/query", nil, map[string]string{"query": in.Query}, &result); err != nil {
		return nil, err
	}
	return queryOutput(result)
}

func executeReadOnlySQL(ctx context.Context, c *call, in sqlArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(in.Query) > schema.MaxReadOnlySQL {
		return nil, fmt.Errorf("read-only SQL is limited to %d bytes", schema.MaxReadOnlySQL)
	}
	var result schema.QueryResult
	if err := c.get(ctx, "/api/schema/"+projectID+"/query", url.Values{"sql": {in.Query}}, &result); err != nil {
		return nil, err
	}
	return queryOutput(result)
}

func queryOutput(result schema.QueryResult) (any, error) {
	if result.Error != "" {
		return nil, fmt.Errorf("the database refused the statement: %s", result.Error)
	}
	rows := result.Rows
	if rows == nil {
		rows = [][]interface{}{}
	}
	return map[string]any{
		"columns": result.Columns, "rows": rows, "truncated": result.Truncated,
		"command": result.Command, "affectedRows": result.AffectedRows,
	}, nil
}

func listMigrations(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var migrations []domain.MigrationRecord
	if err := c.get(ctx, "/api/provision/"+projectID+"/migrations/", nil, &migrations); err != nil {
		return nil, err
	}
	if migrations == nil {
		migrations = []domain.MigrationRecord{}
	}
	return map[string]any{"migrations": migrations}, nil
}

func applyMigration(ctx context.Context, c *call, in migrationArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.SQL) == "" {
		return nil, fmt.Errorf("name and sql are required")
	}
	request := domain.MigrationRequest{Version: in.Version, Name: in.Name, Description: in.Description, SQL: in.SQL}
	var record domain.MigrationRecord
	if err := c.send(ctx, http.MethodPost, "/api/provision/"+projectID+"/migrations/", nil, request, &record); err != nil {
		return nil, err
	}
	return map[string]any{"migration": record}, nil
}

func listPermissions(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var document json.RawMessage
	if err := c.get(ctx, "/api/provision/"+projectID+"/permissions/", nil, &document); err != nil {
		return nil, err
	}
	return map[string]any{"permissions": document}, nil
}

func setPermission(ctx context.Context, c *call, in permissionArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	path, err := permissionPath(projectID, in)
	if err != nil {
		return nil, err
	}
	if in.Remove {
		if err := c.send(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
			return nil, err
		}
		return map[string]any{"removed": true, "table": in.Table, "role": in.Role, "operation": in.Operation}, nil
	}
	if in.Permission == nil {
		return nil, fmt.Errorf("permission is required unless remove is true")
	}
	var saved json.RawMessage
	if err := c.send(ctx, http.MethodPut, path, nil, in.Permission, &saved); err != nil {
		return nil, err
	}
	return map[string]any{"table": in.Table, "role": in.Role, "operation": in.Operation, "permission": saved}, nil
}

func permissionPath(projectID string, in permissionArgs) (string, error) {
	table, err := segment("table", in.Table)
	if err != nil {
		return "", err
	}
	role, err := segment("role", in.Role)
	if err != nil {
		return "", err
	}
	operation, err := segment("operation", in.Operation)
	if err != nil {
		return "", err
	}
	return "/api/provision/" + projectID + "/permissions/tables/" + table + "/roles/" + role + "/" + operation, nil
}
