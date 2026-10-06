package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

const (
	defaultAppPort = 8080
	// statusNotDeployed is what a client sees for an app no deploy has run:
	// the stored status of a new app reads as if a rollout were under way.
	statusNotDeployed = "NOT_DEPLOYED"
	statusCreated     = "PROVISIONING"
	nextDeploy        = "call deploy_app to run it (no image needed: it deploys the image the app names)"
)

type createAppArgs struct {
	projectArg
	Name            string `json:"name" jsonschema:"the app's name; it becomes part of its public address"`
	Image           string `json:"image" jsonschema:"the image to run, e.g. ghcr.io/team/web:main; a tag is pinned to its digest on deploy"`
	Port            int    `json:"port,omitempty" jsonschema:"the port the image serves HTTP on; 8080 when left out"`
	HealthCheckPath string `json:"health_check_path,omitempty" jsonschema:"a path that answers 200 when the app is ready, e.g. /healthz"`
}

// createApp creates a public web app through the same route and plan limits
// as Studio, then allows its own origin in the project's CORS list so the
// page can call the project's APIs from the browser.
func createApp(ctx context.Context, c *call, in createAppArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	port := in.Port
	if port == 0 {
		port = defaultAppPort
	}
	body := map[string]any{"name": in.Name, "image": in.Image, "port": port, "replicas": 1}
	if in.HealthCheckPath != "" {
		body["healthCheckPath"] = in.HealthCheckPath
	}
	var app appView
	if err := c.send(ctx, http.MethodPost, projectsAPI+projectID+"/apps/", nil, body, &app); err != nil {
		return nil, err
	}
	app.Status = statusNotDeployed
	out := map[string]any{"app": app, "next": nextDeploy}
	added, err := allowAppOrigin(ctx, c, projectID, app.URL)
	if err != nil {
		out["corsError"] = "the app was created, but its origin could not be added to the CORS allowlist: " + err.Error()
		return out, nil
	}
	if added != "" {
		out["corsOriginAdded"] = added
	}
	return out, nil
}

type corsList struct {
	AllowedOrigins []string `json:"allowedOrigins"`
	AllowWildcard  bool     `json:"allowWildcard"`
}

// allowAppOrigin adds the origin of appURL to the project's CORS allowlist
// unless it is there already; it answers the origin it added.
func allowAppOrigin(ctx context.Context, c *call, projectID, appURL string) (string, error) {
	origin, err := originOf(appURL)
	if err != nil || origin == "" {
		return "", err
	}
	corsPath := projectsAPI + projectID + "/cors/"
	var current corsList
	if err := c.get(ctx, corsPath, nil, &current); err != nil {
		return "", err
	}
	if current.AllowWildcard || slices.Contains(current.AllowedOrigins, origin) {
		return "", nil
	}
	updated := corsList{AllowedOrigins: append(slices.Clone(current.AllowedOrigins), origin)}
	if err := c.send(ctx, http.MethodPut, corsPath, nil, updated, nil); err != nil {
		return "", err
	}
	return origin, nil
}

func originOf(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("the app's address %q is not a URL", raw)
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

// markNotDeployed reports an app that no deploy has run as NOT_DEPLOYED.
func markNotDeployed(ctx context.Context, c *call, projectID string, app *appView) (bool, error) {
	if app.Status != statusCreated {
		return false, nil
	}
	path, err := appPath(projectID, app.ID)
	if err != nil {
		return false, err
	}
	var deploys []map[string]any
	if err := c.get(ctx, path+"/deploys", url.Values{"limit": {"1"}}, &deploys); err != nil {
		return false, err
	}
	if len(deploys) > 0 {
		return false, nil
	}
	app.Status = statusNotDeployed
	return true, nil
}
