package mcpserver

import (
	"context"
	"fmt"
	"net/http"
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

func functionTools() []entry {
	return []entry{
		tool("list_functions", "List the project's edge functions.", readTool, listFunctions),
		tool("deploy_function", "Create or update an edge function from its source files and deploy it.", writeTool, deployFunction),
		tool("set_function_secret", "Set a secret the project's edge functions read as an environment variable. The value is never returned.", writeTool, setFunctionSecret),
	}
}

func listFunctions(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	functions := []functionView{}
	if err := c.get(ctx, "/api/projects/"+projectID+"/functions/", nil, &functions); err != nil {
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
	if err := c.send(ctx, http.MethodPost, "/api/projects/"+projectID+"/functions/", nil, body, &deployed); err != nil {
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
	if err := c.send(ctx, http.MethodPost, "/api/projects/"+projectID+"/functions/secrets", nil, body, nil); err != nil {
		return nil, err
	}
	return map[string]any{"key": in.Key, "set": true}, nil
}
