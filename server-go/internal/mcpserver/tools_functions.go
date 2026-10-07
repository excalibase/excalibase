package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

type functionFile struct {
	Path    string `json:"path" jsonschema:"file path inside the function, e.g. index.ts"`
	Content string `json:"content"`
}

type deployFunctionArgs struct {
	projectArg
	ID          string         `json:"id" jsonschema:"the function's slug, unique in the project"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Files       []functionFile `json:"files" jsonschema:"the source files; index.ts is the entry point"`
	VerifyJWT   *bool          `json:"verify_jwt,omitempty" jsonschema:"false lets unauthenticated callers in (webhooks that check their own signature); true when left out"`
}

type functionSecretArgs struct {
	projectArg
	Key   string `json:"key" jsonschema:"the secret's name, read in the function as an environment variable"`
	Value string `json:"value"`
}

// functionView is a function without its source.
type functionView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Version      int    `json:"version"`
	Active       bool   `json:"active"`
	VerifyJwt    *bool  `json:"verifyJwt,omitempty"`
	RuntimeShape string `json:"runtimeShape,omitempty"`
	Kind         string `json:"kind,omitempty"`
}

type functionIDArgs struct {
	projectArg
	ID string `json:"id" jsonschema:"the edge function's id (list_functions)"`
}

type outboundHostsArgs struct {
	projectArg
	Add    []string `json:"add,omitempty" jsonschema:"hosts to allow, e.g. [\"api.stripe.com\"]"`
	Remove []string `json:"remove,omitempty" jsonschema:"hosts to stop allowing"`
}

const secretWarning = "WARNING: the value passes through this conversation and the AI tool's logs. Prefer asking the user to set it in Studio " +
	"(Edge Functions → Secrets); use this only for a test value or when the user explicitly asks."

func functionTools() []entry {
	return []entry{
		tool("list_functions", "List the project's edge functions.", readTool, listFunctions),
		tool("deploy_function", "Create or update an edge function from its source files and deploy it. Functions are for APIs and webhooks, not web pages: their answers carry Content-Security-Policy script-src 'self', so a page's inline scripts never run. Host a web page as a container app (create_app).", writeTool, deployFunction),
		tool("delete_function", "Delete one edge function: it stops answering at once.", writeTool, deleteFunction),
		tool("set_function_outbound_hosts", "Allow or stop allowing outside hosts the project's edge functions may call (fetch); anything not listed is blocked. "+
			"Hosts already on the list stay unless removed. The functions are redeployed with the new list.", writeTool, setFunctionOutboundHosts),
		tool("set_function_secret", "Set a secret the project's edge functions read as an environment variable. The value is never returned. "+secretWarning, writeTool, setFunctionSecret),
	}
}

func deleteFunction(ctx context.Context, c *call, in functionIDArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	id, err := segment("id", in.ID)
	if err != nil {
		return nil, err
	}
	if err := c.send(ctx, http.MethodDelete, projectsAPI+projectID+"/functions/"+id+"/", nil, nil, nil); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": in.ID}, nil
}

func setFunctionOutboundHosts(ctx context.Context, c *call, in outboundHostsArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(in.Add)+len(in.Remove) == 0 {
		return nil, errors.New("name hosts to add or remove")
	}
	egressPath := projectsAPI + projectID + "/functions/egress"
	var current struct {
		AllowedHosts []string `json:"allowedHosts"`
	}
	if err := c.get(ctx, egressPath, nil, &current); err != nil {
		return nil, err
	}
	hosts := make([]string, 0, len(current.AllowedHosts)+len(in.Add))
	for _, host := range append(slices.Clone(current.AllowedHosts), in.Add...) {
		if !slices.Contains(in.Remove, host) && !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	var saved map[string]any
	if err := c.send(ctx, http.MethodPut, egressPath, nil, map[string]any{"allowedHosts": hosts}, &saved); err != nil {
		return nil, err
	}
	return map[string]any{"egress": saved, "note": "effectiveHosts is what a function may reach: the operator defaults plus the project's list."}, nil
}

func listFunctions(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	functions := []functionView{}
	if err := c.get(ctx, projectsAPI+projectID+"/functions/", nil, &functions); err != nil {
		return nil, err
	}
	return map[string]any{
		"functions": functions,
		"invokeUrl": strings.TrimRight(c.settings.PublicBaseURL, "/") + "/functions/v1/" + projectID + "/<function id>",
	}, nil
}

func deployFunction(ctx context.Context, c *call, in deployFunctionArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	if in.ID == "" || len(in.Files) == 0 {
		return nil, fmt.Errorf("id and at least one file are required")
	}
	name := in.Name
	if name == "" {
		name = in.ID
	}
	body := map[string]any{"id": in.ID, "name": name, "description": in.Description, "files": in.Files}
	if in.VerifyJWT != nil {
		body["verifyJwt"] = *in.VerifyJWT
	}
	var deployed functionView
	if err := c.send(ctx, http.MethodPost, projectsAPI+projectID+"/functions/", nil, body, &deployed); err != nil {
		return nil, err
	}
	return map[string]any{"function": deployed}, nil
}

func setFunctionSecret(ctx context.Context, c *call, in functionSecretArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	if in.Key == "" {
		return nil, fmt.Errorf("key is required")
	}
	body := map[string]string{"key": in.Key, "value": in.Value}
	if err := c.send(ctx, http.MethodPost, projectsAPI+projectID+"/functions/secrets", nil, body, nil); err != nil {
		return nil, err
	}
	return map[string]any{
		"key": in.Key, "set": true,
		"note": "Next time, ask the user to set secrets in Studio so the value stays out of the conversation: " + studioOnly(c.settings.StudioURL, projectID)["functionSecrets"],
	}, nil
}
