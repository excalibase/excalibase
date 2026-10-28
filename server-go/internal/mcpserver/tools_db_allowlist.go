package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
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
			"or the DocumentDB Mongo port). Anything not listed is refused at the network edge. Hosts and networks up to IPv4 /24 or IPv6 /48; wider is set in Studio. Admins only.", writeTool, setDBAllowedSources)),
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

// A coding tool may name hosts and small networks only; anything wider, and
// allowing every address, is decided in Studio.
const (
	mcpMinIPv4Prefix = 24
	mcpMinIPv6Prefix = 48
)

// mcpAllowListSources refuses wide entries here; the server validates the rest.
func mcpAllowListSources(sources []string) ([]string, error) {
	if len(sources) == 0 {
		return nil, errors.New("name at least one address or range; closing the port or allowing any address is done in Studio")
	}
	for _, source := range sources {
		if err := narrowSource(strings.TrimSpace(source)); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

func narrowSource(source string) error {
	if source == "*" {
		return errors.New("allowing every address is not set through MCP; list the addresses that need the port")
	}
	if !strings.Contains(source, "/") {
		return nil
	}
	prefix, err := netip.ParsePrefix(source)
	if err != nil {
		return fmt.Errorf("%q is not an address or CIDR range", source)
	}
	if (prefix.Addr().Is4() && prefix.Bits() < mcpMinIPv4Prefix) || (prefix.Addr().Is6() && prefix.Bits() < mcpMinIPv6Prefix) {
		return fmt.Errorf("%q is wider than MCP may set (IPv4 /%d, IPv6 /%d); set wider ranges in Studio", source, mcpMinIPv4Prefix, mcpMinIPv6Prefix)
	}
	return nil
}

func allowListAnswer(list dbAllowList) map[string]any {
	return map[string]any{"access": list.Access, "sources": nonNil(list.Sources), "max": list.Max,
		"publicEnabled": list.PublicEnabled, "singleHost": list.SingleHost, "note": dbAllowListNote}
}
