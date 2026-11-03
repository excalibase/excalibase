package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type realtimeArgs struct {
	tableArgs
	Enabled bool `json:"enabled" jsonschema:"true publishes the table's changes to subscriptions; false stops it"`
}

func realtimeTools() []entry {
	return []entry{
		tool("set_realtime", "Turn realtime on or off for one table (table, or schema.table as set_permission takes it). A GraphQL subscription to <field>Changes (get_graphql_schema) "+
			"fires only for a table with realtime on; events still pass through the subscriber's select permission. "+
			"Turning it on sets the table to REPLICA IDENTITY FULL, so deletes carry the old row the permission is checked against; "+
			"each update and delete then writes the whole old row to the WAL.", writeTool, setRealtime),
	}
}

func setRealtime(ctx context.Context, c *call, given realtimeArgs) (any, error) {
	projectID, err := c.useProject(given.ProjectID)
	if err != nil {
		return nil, err
	}
	in, err := qualifiedTable(given)
	if err != nil {
		return nil, err
	}
	schemaName, err := segment("schema", in.schemaName())
	if err != nil {
		return nil, err
	}
	table, err := segment("table", in.Table)
	if err != nil {
		return nil, err
	}
	method := http.MethodDelete
	if in.Enabled {
		method = http.MethodPut
	}
	if err := c.send(ctx, method, projectsAPI+projectID+"/realtime/tables/"+schemaName+"/"+table, nil, nil, nil); err != nil {
		return nil, err
	}
	fields := graphQLFields(in.schemaName(), in.Table, false)
	return map[string]any{
		"schema": in.schemaName(), "table": in.Table, "enabled": in.Enabled, "subscription": fields.Subscription,
	}, nil
}

// qualifiedTable reads a table given as schema.table, the form set_permission
// takes, into its schema and table (EXC-641).
func qualifiedTable(in realtimeArgs) (realtimeArgs, error) {
	schemaName, table, qualified := strings.Cut(in.Table, ".")
	if !qualified {
		return in, nil
	}
	if in.Schema != "" && in.Schema != schemaName {
		return in, fmt.Errorf("table %q names schema %q but schema is %q; pass one of them", in.Table, schemaName, in.Schema)
	}
	out := in
	out.Schema, out.Table = schemaName, table
	return out, nil
}
