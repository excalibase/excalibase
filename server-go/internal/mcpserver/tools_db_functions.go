package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

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
			"Create the function first (apply_migration). A function that only reads (STABLE/IMMUTABLE) is a query, any other a mutation. "+
			"A SECURITY DEFINER function runs with its owner's privileges, so check its arguments.", writeTool, trackDBFunction),
		tool("set_db_function_permission", "Let one role call a tracked database function, or take that away.", writeTool, setDBFunctionPermission),
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
	return map[string]any{"function": in.Function, "role": in.Role, "allowed": in.Allowed}, nil
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
