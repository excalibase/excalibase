package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type corsOriginArgs struct {
	projectArg
	Origin string `json:"origin" jsonschema:"one origin: scheme://host[:port] with no path, e.g. http://localhost:5173 or https://shop.example.com"`
}

// corsEdit is the answer of an origin add or remove.
type corsEdit struct {
	corsList
	Added   bool `json:"added"`
	Removed bool `json:"removed"`
}

const corsNote = "A browser page can call the project's APIs (REST, GraphQL, auth sign-in, functions) only from an origin on this list. " +
	"Changes reach the data plane within about a minute."

func corsTools() []entry {
	return []entry{
		tool("list_cors_origins", "The project's CORS allowlist: the browser origins that may call its APIs.", readTool, listCorsOrigins),
		tool("add_cors_origin", "Allow one browser origin to call the project's APIs, e.g. a local dev server (http://localhost:5173) or a custom domain. "+
			"Native apps run in a WebView and need their origin too: capacitor://localhost (Capacitor iOS), https://localhost (Capacitor Android), "+
			"tauri://localhost (Tauri macOS/Linux), http://tauri.localhost (Tauri Windows), or an Electron app's custom protocol; "+
			"an Electron file:// page sends Origin null, which cannot be listed, so register a custom protocol instead. "+
			"Other origins on the list are kept.", writeTool, addCorsOrigin),
		tool("remove_cors_origin", "Stop allowing one browser origin; the rest of the list is kept.", writeTool, removeCorsOrigin),
	}
}

func listCorsOrigins(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var current corsList
	if err := c.get(ctx, projectsAPI+projectID+"/cors/", nil, &current); err != nil {
		return nil, err
	}
	return map[string]any{"allowedOrigins": nonNil(current.AllowedOrigins), "allowWildcard": current.AllowWildcard, "note": corsNote}, nil
}

func addCorsOrigin(ctx context.Context, c *call, in corsOriginArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	origin, err := oneOrigin(in.Origin)
	if err != nil {
		return nil, err
	}
	var edit corsEdit
	if err := c.send(ctx, http.MethodPost, projectsAPI+projectID+"/cors/origins", nil, map[string]string{"origin": origin}, &edit); err != nil {
		return nil, err
	}
	return map[string]any{"added": edit.Added, "allowedOrigins": nonNil(edit.AllowedOrigins), "allowWildcard": edit.AllowWildcard, "note": corsNote}, nil
}

func removeCorsOrigin(ctx context.Context, c *call, in corsOriginArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	origin, err := oneOrigin(in.Origin)
	if err != nil {
		return nil, err
	}
	var edit corsEdit
	if err := c.send(ctx, http.MethodDelete, projectsAPI+projectID+"/cors/origins", url.Values{"origin": {origin}}, nil, &edit); err != nil {
		return nil, err
	}
	return map[string]any{"removed": edit.Removed, "allowedOrigins": nonNil(edit.AllowedOrigins), "note": corsNote}, nil
}

// oneOrigin refuses the wildcard here: opening a project to every origin is
// a decision made in Studio, not by a coding tool.
func oneOrigin(raw string) (string, error) {
	origin := strings.TrimSpace(raw)
	if origin == "" || strings.Contains(origin, "*") {
		return "", errors.New("origin must be one origin, e.g. http://localhost:5173; the wildcard is not set through MCP")
	}
	return origin, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
