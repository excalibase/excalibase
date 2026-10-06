package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type projectArg struct {
	ProjectID string `json:"project_id,omitempty" jsonschema:"the project id; may be left out when the connection is limited to one project"`
}

type noArgs struct{}

type projectSummary struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName,omitempty"`
	Status      string `json:"status,omitempty"`
	OrgID       string `json:"orgId,omitempty"`
	NoDatabase  bool   `json:"noDatabase,omitempty"`
}

type sdkKey struct {
	ID        int64  `json:"id"`
	KeyPrefix string `json:"keyPrefix"`
	KeyType   string `json:"keyType"`
	Name      string `json:"name"`
}

type createKeyArgs struct {
	projectArg
	Name string `json:"name" jsonschema:"a name for the key, e.g. web"`
}

func projectTools() []entry {
	return []entry{
		tool("list_projects", "List the projects this connection can reach, with their ids and status.", readTool, listProjects),
		tool("get_project_info", "Endpoints for the project's GraphQL and REST APIs, its publishable SDK keys, and a setup snippet for @excalibase/sdk.", readTool, getProjectInfo),
		tool("create_publishable_key", "Create a publishable SDK key (role anon, safe to ship in a browser bundle). The full key is returned once.", writeTool, createPublishableKey),
	}
}

func listProjects(ctx context.Context, c *call, _ noArgs) (any, error) {
	var projects []projectSummary
	if err := c.get(ctx, provisionAPI, nil, &projects); err != nil {
		return nil, err
	}
	visible := make([]projectSummary, 0, len(projects))
	for _, project := range projects {
		if c.caller.Project == "" || project.ProjectID == c.caller.Project {
			visible = append(visible, project)
		}
	}
	if c.caller.Project != "" {
		c.project = c.caller.Project
	}
	return map[string]any{"projects": visible}, nil
}

func getProjectInfo(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var info struct {
		ProjectID          string   `json:"projectId"`
		ProjectName        string   `json:"projectName"`
		OrgSlug            string   `json:"orgSlug"`
		CorsAllowedOrigins []string `json:"corsAllowedOrigins"`
	}
	if err := c.get(ctx, projectsAPI+projectID+"/info/", nil, &info); err != nil {
		return nil, err
	}
	urls := endpoints(c.settings.PublicBaseURL, info.OrgSlug, projectID)
	setup := sdkSetup(c.settings.PublicBaseURL, projectID)
	setup["fetchExample"] = fetchExample(urls)
	origins := info.CorsAllowedOrigins
	if origins == nil {
		origins = []string{}
	}
	out := map[string]any{
		"projectId": projectID, "projectName": info.ProjectName,
		"endpoints": urls,
		"sdkSetup":  setup,
		"restApi":   restGuide(urls),
		"cors": map[string]any{
			"allowedOrigins": origins,
			"note": "A browser page can call these APIs only from an origin in this list. create_app adds an app's own origin; " +
				"any other origin (a local dev server, a custom domain) is added in Studio, Settings, CORS.",
		},
	}
	keys, err := publishableKeys(ctx, c, projectID)
	if err != nil {
		var routeErr *RouteError
		if !errors.As(err, &routeErr) || routeErr.Status != http.StatusServiceUnavailable {
			return nil, err
		}
		out["publishableKeysError"] = routeErr.Message
	}
	out["publishableKeys"] = keys
	if len(keys) == 0 {
		out["publishableKeysNote"] = "No publishable key exists yet; create one with create_publishable_key. A key's full value is shown only when it is created."
	} else {
		out["publishableKeysNote"] = "Only the prefix of an existing key can be read back; use the full key saved when it was created, or create a new one."
	}
	return out, nil
}

func publishableKeys(ctx context.Context, c *call, projectID string) ([]sdkKey, error) {
	var listed struct {
		Keys []sdkKey `json:"keys"`
	}
	if err := c.get(ctx, projectsAPI+projectID+"/sdk-keys/", nil, &listed); err != nil {
		return []sdkKey{}, err
	}
	keys := make([]sdkKey, 0, len(listed.Keys))
	for _, key := range listed.Keys {
		if key.KeyType == "publishable" {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func endpoints(base, orgSlug, projectID string) map[string]string {
	base = strings.TrimRight(base, "/")
	if orgSlug == "" {
		orgSlug = projectID
	}
	realtime := strings.Replace(base, "http", "ws", 1) + "/" + projectID + "/graphql"
	return map[string]string{
		"baseUrl":   base,
		"graphql":   base + "/" + projectID + "/graphql",
		"rest":      base + "/" + projectID + "/api/v1",
		"auth":      base + "/auth/" + orgSlug + "/" + projectID,
		"functions": base + "/functions/v1/" + projectID,
		"realtime":  realtime,
	}
}

func sdkSetup(base, projectID string) map[string]string {
	snippet := fmt.Sprintf(`import { createClient } from "@excalibase/sdk";

export const db = createClient({
  url: %q,
  projectId: %q,
  // A publishable key (esk_pub_...), safe in browser code.
  key: process.env.EXCALIBASE_PUBLISHABLE_KEY,
});

await db.auth.signInWithApiKey();
const data = await db.graphql.query("{ __typename }");`, strings.TrimRight(base, "/"), projectID)
	return map[string]string{
		"install": "npm install @excalibase/sdk graphql-request",
		"snippet": snippet,
		"types":   "npx excalibase-codegen --url " + strings.TrimRight(base, "/") + " --project " + projectID + " --key <publishable key> --out src/database.types.ts",
	}
}

// fetchExample is the SDK's sign-in and one REST call with plain fetch, for a
// page built without a bundler.
func fetchExample(urls map[string]string) string {
	return fmt.Sprintf(`const KEY = "esk_pub_..."; // a publishable key, safe in a page

const signIn = await fetch(%q, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ grant_type: "api_key", api_key: KEY }),
});
const { accessToken } = await signIn.json();

// REST: one path per table, e.g. GET <rest>/todos; POST, PATCH and DELETE write.
const rows = await fetch(%q, {
  headers: { Authorization: "Bearer " + accessToken, "X-Excalibase-Publishable-Key": KEY },
}).then((answer) => answer.json());`, urls["auth"]+"/token", urls["rest"]+"/todos")
}

func createPublishableKey(ctx context.Context, c *call, in createKeyArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	var created map[string]any
	body := map[string]string{"keyType": "publishable", "name": name}
	if err := c.send(ctx, http.MethodPost, projectsAPI+projectID+"/sdk-keys/", nil, body, &created); err != nil {
		return nil, err
	}
	return map[string]any{"key": created, "note": "Save the plaintext now; it is not shown again."}, nil
}
