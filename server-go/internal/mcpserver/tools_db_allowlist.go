package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/features"
)

type dbAllowListArgs struct {
	projectArg
	Sources []string `json:"sources" jsonschema:"addresses or CIDR ranges that may reach the public database port, e.g. 203.0.113.7 or 198.51.100.0/24; replaces the list"`
}

// dbAllowList is the route's answer, without the caller address the router
// sees for this server.
type dbAllowList struct {
	Access        string   `json:"access"`
	Sources       []string `json:"sources"`
	Max           int      `json:"max"`
	PublicEnabled bool     `json:"publicEnabled"`
	SingleHost    bool     `json:"singleHost,omitempty"`
}

const dbAllowListNote = "access is unset (never chosen: an already open port admits any address), any, listed, or host (a single host's database, on its loopback only). " +
	"Allowing any address is a decision made in Studio, not through MCP. The list reaches the database's public port before it is saved."

func dbAllowListTools() []entry {
	return []entry{
		dark(features.DBAllowList, tool("get_db_allowed_sources", "Who may reach the project's public database port: the allowed addresses and ranges, "+
			"and whether the port is open.", readTool, getDBAllowedSources)),
		dark(features.DBAllowList, tool("set_db_allowed_sources", "Replace the addresses and CIDR ranges that may reach the project's public database port (Postgres, MySQL "+
			"or the DocumentDB Mongo port). Anything not listed is refused at the network edge. Admins only.", writeTool, setDBAllowedSources)),
	}
}

func allowListPath(projectID string) string {
	return projectsAPI + projectID + "/db-endpoint/allowed-sources/"
}

func getDBAllowedSources(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var current dbAllowList
	if err := c.get(ctx, allowListPath(projectID), nil, &current); err != nil {
		return nil, err
	}
	return allowListAnswer(current), nil
}

func setDBAllowedSources(ctx context.Context, c *call, in dbAllowListArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	sources, err := mcpAllowListSources(in.Sources)
	if err != nil {
		return nil, err
	}
	var saved dbAllowList
	if err := c.send(ctx, http.MethodPut, allowListPath(projectID), nil, map[string]any{"sources": sources}, &saved); err != nil {
		return nil, err
	}
	return allowListAnswer(saved), nil
}

// mcpAllowListSources refuses "every address" here; the server validates the rest.
func mcpAllowListSources(sources []string) ([]string, error) {
	if len(sources) == 0 {
		return nil, errors.New("name at least one address or range; closing the port or allowing any address is done in Studio")
	}
	for _, source := range sources {
		trimmed := strings.TrimSpace(source)
		if trimmed == "*" || strings.HasSuffix(trimmed, "/0") {
			return nil, errors.New("allowing every address is not set through MCP; list the addresses that need the port")
		}
	}
	return sources, nil
}

func allowListAnswer(list dbAllowList) map[string]any {
	return map[string]any{"access": list.Access, "sources": nonNil(list.Sources), "max": list.Max,
		"publicEnabled": list.PublicEnabled, "singleHost": list.SingleHost, "note": dbAllowListNote}
}
