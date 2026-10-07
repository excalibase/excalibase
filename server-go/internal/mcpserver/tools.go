package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// access says which connections a tool is offered on.
type access int

const (
	// readTool is offered on every connection.
	readTool access = iota
	// writeTool changes the project; a read-only connection never sees it.
	writeTool
	// readOnlyVariant is the read-only form of a write tool of the same name.
	readOnlyVariant
)

// entry is one tool in the catalogue.
type entry struct {
	name   string
	access access
	add    func(server *mcp.Server, e *env, caller Caller)
}

func (t entry) offeredTo(caller Caller) bool {
	switch t.access {
	case writeTool:
		return !caller.ReadOnly
	case readOnlyVariant:
		return caller.ReadOnly
	}
	return true
}

// tool declares one entry. run returns the tool's structured result; a route
// refusal or a failed call comes back as a tool error.
func tool[In any](name, description string, level access, run func(context.Context, *call, In) (any, error)) entry {
	return entry{name: name, access: level, add: func(server *mcp.Server, e *env, caller Caller) {
		definition := &mcp.Tool{
			Name: name, Description: description,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: level != writeTool},
		}
		mcp.AddTool(server, definition, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			c := &call{dispatcher: dispatcher{router: e.router, caller: caller, answered: &[]routeReply{}}, settings: e.settings, probes: e.probes}
			if caller.Refused != "" {
				err := errors.New(caller.Refused)
				e.record(ctx, caller, name, "", err)
				return nil, nil, err
			}
			out, err := run(ctx, c, in)
			e.record(ctx, caller, name, c.answeredProject(), err)
			if err != nil {
				return nil, nil, err
			}
			return nil, out, nil
		})
	}}
}

// The route trees tools call; a project id follows each.
const (
	projectsAPI  = "/api/projects/"
	provisionAPI = "/api/provision/"
	schemaAPI    = "/api/schema/"
)

func tablePath(projectID, table string) string {
	return schemaAPI + projectID + "/tables/" + table
}

// catalogue is every tool the endpoint offers.
func catalogue() []entry {
	tools := make([]entry, 0, 32)
	tools = append(tools, projectTools()...)
	tools = append(tools, corsTools()...)
	tools = append(tools, schemaTools()...)
	tools = append(tools, realtimeTools()...)
	tools = append(tools, databaseTools()...)
	tools = append(tools, dbFunctionTools()...)
	tools = append(tools, functionTools()...)
	tools = append(tools, appTools()...)
	tools = append(tools, shippingTools()...)
	tools = append(tools, probeTools()...)
	return tools
}

// call is one tool invocation: the dispatcher plus the project it resolved.
type call struct {
	dispatcher
	settings Settings
	probes   *probeLimits
	project  string
}

// useProject resolves and remembers the project this call acts on.
func (c *call) useProject(requested string) (string, error) {
	projectID, err := c.caller.resolveProject(requested)
	if err != nil {
		return "", err
	}
	c.project = projectID
	return projectID, nil
}

// answeredProject is the project this call is filed under: the one it
// resolved, and only once one of that project's routes answered past the
// access gate (anything but 401 or 404). Naming a project is not enough, or
// anyone could write into another project's activity.
func (c *call) answeredProject() string {
	if c.project == "" || c.answered == nil {
		return ""
	}
	for _, reply := range *c.answered {
		if reply.status == http.StatusUnauthorized || reply.status == http.StatusNotFound {
			continue
		}
		if segments := strings.Split(reply.path, "/"); len(segments) > 3 && segments[1] == "api" && segments[3] == c.project {
			return c.project
		}
	}
	return ""
}

func (c *call) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.send(ctx, http.MethodGet, path, query, nil, out)
}

// segment turns a tool argument into one escaped path segment. It can never
// climb out of the route it is placed in.
func segment(argument, value string) (string, error) {
	if value == "" || value == "." || value == ".." || len(value) > 256 {
		return "", fmt.Errorf("%s is required and must name one object", argument)
	}
	return url.PathEscape(value), nil
}
