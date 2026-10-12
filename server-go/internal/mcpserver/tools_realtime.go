package mcpserver

import (
	"context"
	"net/http"
)

type realtimeArgs struct {
	tableArgs
	Enabled bool `json:"enabled" jsonschema:"true publishes the table's changes to subscriptions; false stops it"`
}

func realtimeTools() []entry {
	return []entry{
		tool("set_realtime", "Turn realtime on or off for one table. A GraphQL subscription to <field>Changes (get_graphql_schema) "+
			"fires only for a table with realtime on; events still pass through the subscriber's select permission. "+
			"Turning it on sets the table to REPLICA IDENTITY FULL, so deletes carry the old row the permission is checked against; "+
			"each update and delete then writes the whole old row to the WAL.", writeTool, setRealtime),
	}
}

func setRealtime(ctx context.Context, c *call, in realtimeArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
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
