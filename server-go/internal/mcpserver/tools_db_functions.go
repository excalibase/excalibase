package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/routepolicy"
)

// exposedAsQuery is how a tracked function that only reads is served.
const exposedAsQuery = "QUERY"

type trackFunctionArgs struct {
	projectArg
	Function         string   `json:"function" jsonschema:"the database function, schema-qualified, e.g. public.short_code"`
	Roles            []string `json:"roles,omitempty" jsonschema:"roles allowed to call it, e.g. [\"anon\", \"user\"]; none when left out"`
	InferPermissions *bool    `json:"infer_permissions,omitempty" jsonschema:"true (default): the rows it returns obey the return table's select permission"`
	SessionArgument  *string  `json:"session_argument,omitempty" jsonschema:"an argument that receives the caller's session variables as JSON, if the function takes one"`
	Remove           bool     `json:"remove,omitempty" jsonschema:"true stops serving the function through the API (the function stays in the database)"`
}

type functionPermissionArgs struct {
	projectArg
	Function string `json:"function" jsonschema:"the tracked database function, e.g. public.short_code"`
	Role     string `json:"role" jsonschema:"the API role, e.g. anon or user"`
	Allowed  bool   `json:"allowed" jsonschema:"true lets the role call the function; false takes that away"`
}

func dbFunctionTools() []entry {
	return []entry{
		tool("track_db_function", "Serve a database function through the API (REST <rest>/rpc/<name>, GraphQL root field) and let the given roles call it. "+
			"The function must return rows of a table or view (RETURNS SETOF <table|view>, or one <table|view> row): "+
			"RETURNS TABLE(...) is refused with a 400, because permissions apply to a row type. For a custom shape, "+
			"CREATE VIEW with those columns, give the view a select permission, and return SETOF that view. "+
			"In GraphQL its arguments go directly on the field (publicMySearch(search_text: \"x\")), not in Hasura's args: {...}. "+
			"Create the function first (apply_migration). A function that only reads (STABLE/IMMUTABLE) is a query, any other a mutation. "+
			"A SECURITY DEFINER function runs with its owner's privileges, so check its arguments. "+
			"Tracked functions are not offered on MySQL projects: MySQL functions return scalars, and permissions apply to rows.", writeTool, trackDBFunction),
		tool("set_db_function_permission", "Let one role call a tracked database function, or take that away. "+
			"A query function (STABLE/IMMUTABLE) tracked with infer_permissions on (the default) is callable by every role that may select its "+
			"return table, with or without this permission, so taking it away has no effect while inference is on: remove the role's select "+
			"on the return table, or track the function again with infer_permissions false. Mutations always need this permission.", writeTool, setDBFunctionPermission),
	}
}

func trackDBFunction(ctx context.Context, c *call, in trackFunctionArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	function, err := functionKey(in.Function)
	if err != nil {
		return nil, err
	}
	if in.Remove {
		if err := c.send(ctx, http.MethodDelete, provisionAPI+projectID+"/tracked-functions/"+function, nil, nil, nil); err != nil {
			return nil, err
		}
		return map[string]any{"function": in.Function, "tracked": false}, nil
	}
	for _, role := range in.Roles {
		if _, err := segment("role", role); err != nil {
			return nil, err
		}
	}
	// A 409 below means "already tracked"; a MySQL project is refused first.
	engine, err := projectEngine(ctx, c, projectID)
	if err != nil {
		return nil, err
	}
	if engine == engineMySQL {
		return nil, errors.New(routepolicy.MySQLNoTrackedFunctions)
	}
	body := map[string]any{"function": in.Function, "inferPermissions": in.InferPermissions, "sessionArgument": in.SessionArgument}
	if in.InferPermissions == nil {
		body["inferPermissions"] = true
	}
	var tracked map[string]any
	err = c.send(ctx, http.MethodPost, provisionAPI+projectID+"/tracked-functions/", nil, body, &tracked)
	var routeErr *RouteError
	alreadyTracked := errors.As(err, &routeErr) && routeErr.Status == http.StatusConflict
	if err != nil && !alreadyTracked {
		return nil, err
	}
	granted := make([]string, 0, len(in.Roles))
	for _, role := range in.Roles {
		if err := grantFunction(ctx, c, projectID, function, role, true); err != nil {
			return nil, fmt.Errorf("%s is tracked and %v may call it, but granting %s failed: %w", in.Function, granted, role, err)
		}
		granted = append(granted, role)
	}
	name := in.Function[strings.Index(in.Function, ".")+1:]
	return map[string]any{
		"function": tracked, "alreadyTracked": alreadyTracked, "granted": granted,
		"graphqlField": functionField(in.Function), "rest": "<rest>/rpc/" + name,
	}, nil
}

func setDBFunctionPermission(ctx context.Context, c *call, in functionPermissionArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	function, err := functionKey(in.Function)
	if err != nil {
		return nil, err
	}
	if err := grantFunction(ctx, c, projectID, function, in.Role, in.Allowed); err != nil {
		return nil, err
	}
	out := map[string]any{"function": in.Function, "role": in.Role, "allowed": in.Allowed}
	if !in.Allowed && inferredQuery(ctx, c, projectID, in.Function) {
		out["effect"] = "no effect while infer_permissions is on: " + in.Function + " is a query function tracked with inference, so " +
			in.Role + " can still call it as long as it may select the function's return table. To stop it, remove " + in.Role +
			"'s select on that table (set_permission), or track the function again with infer_permissions false."
	}
	return out, nil
}

// inferredQuery is true when the function is a query tracked with
// inference, which the engine lets any role that may select its return
// table call. A document that cannot be read says nothing.
func inferredQuery(ctx context.Context, c *call, projectID, function string) bool {
	var document domain.PermissionDocument
	if err := c.get(ctx, provisionAPI+projectID+"/permissions/", nil, &document); err != nil {
		log.Printf("mcp: permissions of %s: %v", projectID, err)
		return false
	}
	for _, tracked := range document.Functions {
		if tracked.Function == function {
			return tracked.InferPermissions && tracked.ExposedAs == exposedAsQuery
		}
	}
	return false
}

func grantFunction(ctx context.Context, c *call, projectID, function, role string, allowed bool) error {
	roleSegment, err := segment("role", role)
	if err != nil {
		return err
	}
	method := http.MethodDelete
	if allowed {
		method = http.MethodPut
	}
	err = c.send(ctx, method, provisionAPI+projectID+"/function-permissions/"+function+"/roles/"+roleSegment, nil, nil, nil)
	var routeErr *RouteError
	if !allowed && errors.As(err, &routeErr) && routeErr.Status == http.StatusNotFound {
		return nil // never granted: already what was asked
	}
	return err
}

func functionKey(function string) (string, error) {
	if !strings.Contains(function, ".") {
		return "", errors.New("function must be schema-qualified, e.g. public.short_code")
	}
	return segment("function", function)
}
