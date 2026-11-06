package mcpserver

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// Every tool here is a read: what an agent checks first when a user asks why
// the project is slow, failing or out of room.

type requestMetricsArgs struct {
	projectArg
	Range string `json:"range,omitempty" jsonschema:"1h (the default), 24h or 7d"`
}

type metricsArgs struct {
	projectArg
	History int `json:"history,omitempty" jsonschema:"also return this many past samples; none when left out"`
}

func monitoringTools() []entry {
	return []entry{
		dark(features.RequestMetrics, tool("get_request_metrics", "The project's API traffic over a range: requests, errors and latency percentiles (p50, p95, p99) over time, "+
			"for GraphQL and auth. Read-only.", readTool, getRequestMetrics)),
		tool("get_metrics", "The project database's CPU and memory now (Postgres or MySQL), and with history the recent samples. Read-only.", readTool, getMetrics),
		tool("list_alerts", "The project's active alerts (resource pressure, failing backups and the like). Read-only.", readTool, listAlerts),
		tool("get_database_info", "The project database's disk, size, plan and tunable settings; use it when a disk fills up. No secrets. Postgres only: a MySQL project is refused.",
			readTool, getDatabaseInfo),
		tool("get_db_endpoint", "Where to connect to the project's database from outside: host, port, TLS and CA certificate, and connection strings that carry no password. "+
			"Opening or closing the public port is an Admin's choice in Studio.", readTool, getDBEndpoint),
		dark(features.DBReadAddress, tool("get_db_read_address", "The database's read-only address to its replicas, internal and public, and whether it is on. Read-only.",
			readTool, getDBReadAddress)),
	}
}

func rawRead(ctx context.Context, c *call, path string, query url.Values) (map[string]any, error) {
	var out map[string]any
	if err := c.get(ctx, path, query, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func getRequestMetrics(ctx context.Context, c *call, in requestMetricsArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var query url.Values
	if in.Range != "" {
		query = url.Values{"range": {in.Range}}
	}
	return rawRead(ctx, c, provisionAPI+projectID+"/requests", query)
}

func getMetrics(ctx context.Context, c *call, in metricsArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	current, err := rawRead(ctx, c, provisionAPI+projectID+"/metrics/current", nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"current": current}
	if in.History > 0 {
		var history json.RawMessage
		query := url.Values{"limit": {strconv.Itoa(in.History)}}
		if err := c.get(ctx, provisionAPI+projectID+"/metrics/history", query, &history); err != nil {
			return nil, err
		}
		out["history"] = history
	}
	return out, nil
}

func listAlerts(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var alerts json.RawMessage
	if err := c.get(ctx, "/api/alerts/project/"+projectID+"/", nil, &alerts); err != nil {
		return nil, err
	}
	return map[string]any{"alerts": alerts}, nil
}

func getDatabaseInfo(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	return rawRead(ctx, c, provisionAPI+projectID+"/cluster", nil)
}

func getDBEndpoint(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	return rawRead(ctx, c, projectsAPI+projectID+"/db-endpoint/", nil)
}

func getDBReadAddress(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	return rawRead(ctx, c, projectsAPI+projectID+"/db-read-address/", nil)
}
