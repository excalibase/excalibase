package mcpserver

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

// maxTypedTables bounds how many tables one schema read walks column by column.
const maxTypedTables = 200

type schemaArgs struct {
	projectArg
	Schema string `json:"schema,omitempty" jsonschema:"the database schema; public when left out"`
}

type tableArgs struct {
	schemaArgs
	Table string `json:"table" jsonschema:"the table name"`
}

func (a schemaArgs) schemaName() string {
	if a.Schema == "" {
		return "public"
	}
	return a.Schema
}

func (a schemaArgs) query() url.Values {
	return url.Values{"schema": {a.schemaName()}}
}

func schemaTools() []entry {
	return []entry{
		tool("list_tables", "List the tables and views in a schema of the project's database.", readTool, listTables),
		tool("describe_table", "Columns, indexes, CHECK constraints and foreign keys of one table.", readTool, describeTable),
		tool("get_graphql_schema",
			"What the project's GraphQL and REST APIs are generated from: the endpoint, every table with its columns, foreign keys, "+
				"its exact GraphQL root field names (query, connection, aggregate, mutations, subscription ...Changes), REST path and realtime state. "+
				"The engine builds the GraphQL schema per role from these tables and the project's permissions; the returned introspection command fetches the exact schema a role sees.",
			readTool, getGraphQLSchema),
		tool("generate_typescript_types", "TypeScript row types for every table in a schema, generated from the database columns.", readTool, generateTypeScriptTypes),
	}
}

func listTables(ctx context.Context, c *call, in schemaArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var tables []schema.TableInfo
	if err := c.get(ctx, schemaAPI+projectID+"/tables", in.query(), &tables); err != nil {
		return nil, err
	}
	return map[string]any{"schema": in.schemaName(), "tables": tables}, nil
}

func describeTable(ctx context.Context, c *call, in tableArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	table, err := segment("table", in.Table)
	if err != nil {
		return nil, err
	}
	base := schemaAPI + projectID
	var columns []schema.ColumnInfo
	if err := c.get(ctx, tablePath(projectID, table)+"/columns", in.query(), &columns); err != nil {
		return nil, err
	}
	var indexes []schema.IndexInfo
	if err := c.get(ctx, tablePath(projectID, table)+"/indexes", in.query(), &indexes); err != nil {
		return nil, err
	}
	var checks []schema.CheckConstraint
	if err := c.get(ctx, tablePath(projectID, table)+"/checks", in.query(), &checks); err != nil {
		return nil, err
	}
	var relationships []schema.RelationshipInfo
	if err := c.get(ctx, base+"/relationships", in.query(), &relationships); err != nil {
		return nil, err
	}
	outgoing, incoming := relationsOf(in.Table, relationships)
	return map[string]any{
		"schema": in.schemaName(), "table": in.Table, "columns": columns, "indexes": indexes,
		"checks": checks, "foreignKeys": outgoing, "referencedBy": incoming,
	}, nil
}

func relationsOf(table string, all []schema.RelationshipInfo) (outgoing, incoming []schema.RelationshipInfo) {
	outgoing, incoming = []schema.RelationshipInfo{}, []schema.RelationshipInfo{}
	for _, rel := range all {
		if rel.SourceTable == table {
			outgoing = append(outgoing, rel)
		}
		if rel.TargetTable == table {
			incoming = append(incoming, rel)
		}
	}
	return outgoing, incoming
}

// tableColumns is one table with the columns the routes report for it.
type tableColumns struct {
	Name    string              `json:"name"`
	Type    string              `json:"type"`
	Columns []schema.ColumnInfo `json:"columns"`
}

// schemaTable is one table as get_graphql_schema answers it: its columns, the
// root fields the engine generates for it, its REST path and whether its
// changes are published to subscriptions.
type schemaTable struct {
	tableColumns
	GraphQL  rootFields `json:"graphql"`
	REST     string     `json:"rest"`
	Realtime *bool      `json:"realtime,omitempty"`
}

const namingRule = "Root fields are the engine's names for the schema-qualified table: lowerCamel(schema) + PascalCase(table), " +
	"e.g. public.kanban_cards → publicKanbanCards (list), publicKanbanCardsConnection, publicKanbanCardsAggregate, " +
	"createPublicKanbanCards, createManyPublicKanbanCards (inputs), updatePublicKanbanCards (where, input), deletePublicKanbanCards (where), " +
	"and the subscription publicKanbanCardsChanges. A role sees only the fields its permissions allow: the connection needs the primary key, " +
	"the aggregate needs allowAggregations, each mutation its own permission, the subscription realtime on the table (set_realtime)."

// realtimeTables reads which tables publish changes; nil when that cannot be read.
func realtimeTables(ctx context.Context, c *call, projectID string) map[string]bool {
	var states []struct {
		Schema  string `json:"schema"`
		Table   string `json:"table"`
		Enabled bool   `json:"enabled"`
	}
	if err := c.get(ctx, projectsAPI+projectID+"/realtime/tables", nil, &states); err != nil {
		return nil
	}
	enabled := make(map[string]bool, len(states))
	for _, state := range states {
		enabled[state.Schema+"."+state.Table] = state.Enabled
	}
	return enabled
}

func describeSchemaTables(schemaName, restBase string, tables []tableColumns, realtime map[string]bool) []schemaTable {
	out := make([]schemaTable, 0, len(tables))
	for _, table := range tables {
		entry := schemaTable{
			tableColumns: table,
			GraphQL:      graphQLFields(schemaName, table.Name, strings.EqualFold(table.Type, "VIEW")),
			REST:         restBase + "/" + table.Name,
		}
		if realtime != nil {
			enabled := realtime[schemaName+"."+table.Name]
			entry.Realtime = &enabled
		}
		out = append(out, entry)
	}
	return out
}

// readTables lists a schema's tables and reads each one's columns.
func readTables(ctx context.Context, c *call, projectID string, in schemaArgs, beforeColumns func() error) ([]tableColumns, error) {
	var tables []schema.TableInfo
	if err := c.get(ctx, schemaAPI+projectID+"/tables", in.query(), &tables); err != nil {
		return nil, err
	}
	if len(tables) > maxTypedTables {
		return nil, fmt.Errorf("the schema has %d tables; at most %d are read at once", len(tables), maxTypedTables)
	}
	if beforeColumns != nil {
		if err := beforeColumns(); err != nil {
			return nil, err
		}
	}
	out := make([]tableColumns, 0, len(tables))
	for _, table := range tables {
		name, err := segment("table", table.Name)
		if err != nil {
			return nil, err
		}
		var columns []schema.ColumnInfo
		if err := c.get(ctx, tablePath(projectID, name)+"/columns", in.query(), &columns); err != nil {
			return nil, err
		}
		out = append(out, tableColumns{Name: table.Name, Type: table.Type, Columns: columns})
	}
	return out, nil
}

func getGraphQLSchema(ctx context.Context, c *call, in schemaArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var relationships []schema.RelationshipInfo
	tables, err := readTables(ctx, c, projectID, in, func() error {
		return c.get(ctx, schemaAPI+projectID+"/relationships", in.query(), &relationships)
	})
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(c.settings.PublicBaseURL, "/")
	endpoint := base + "/" + projectID + "/graphql"
	restBase := base + "/" + projectID + "/api/v1"
	return map[string]any{
		"endpoint": endpoint, "schema": in.schemaName(), "foreignKeys": relationships,
		"tables":   describeSchemaTables(in.schemaName(), restBase, tables, realtimeTables(ctx, c, projectID)),
		"naming":   namingRule,
		"realtime": realtimeGuide(endpoint),
		"roles": "Each role sees only the tables, columns and functions it has a permission for (list_permissions); " +
			"anon is a request with no signed-in user (a publishable key's token, or no token at all).",
		"introspection": map[string]string{
			"codegen": "npx excalibase-codegen --url " + base + " --project " + projectID + " --key <publishable key> --out src/database.types.ts",
		},
	}, nil
}

func generateTypeScriptTypes(ctx context.Context, c *call, in schemaArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	tables, err := readTables(ctx, c, projectID, in, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path": "src/database.types.ts", "typescript": typeScriptFor(in.schemaName(), tables), "tables": len(tables),
	}, nil
}
