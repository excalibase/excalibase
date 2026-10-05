package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const (
	defaultDeployHistory = 5
	defaultLogLines      = 200
)

type appArgs struct {
	projectArg
	AppID string `json:"app_id" jsonschema:"the app's id (list_apps)"`
}

type deployAppArgs struct {
	appArgs
	Image     string `json:"image,omitempty" jsonschema:"the image to run, by digest (registry/repo@sha256:...) or tag, resolved to a digest; left out redeploys the app's current image"`
	CommitSHA string `json:"commit_sha,omitempty" jsonschema:"the commit the image was built from, kept with the deploy"`
}

type deployStatusArgs struct {
	appArgs
	DeployID string `json:"deploy_id,omitempty" jsonschema:"one deploy to follow (the id deploy_app returned); the recent deploys when left out"`
	Limit    int    `json:"limit,omitempty" jsonschema:"how many recent deploys to return; 5 when left out"`
}

type logArgs struct {
	projectArg
	Source     string `json:"source" jsonschema:"database, app or function"`
	AppID      string `json:"app_id,omitempty" jsonschema:"the app, when source is app"`
	FunctionID string `json:"function_id,omitempty" jsonschema:"the function, when source is function"`
	Lines      int    `json:"lines,omitempty" jsonschema:"how many recent lines; 200 when left out"`
	Since      string `json:"since,omitempty" jsonschema:"app logs after this RFC 3339 time, or function logs after this Unix time in milliseconds"`
}

// appView is an app without its environment, which can name secrets.
type appView struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Image            string          `json:"image"`
	ResolvedDigest   string          `json:"resolvedDigest,omitempty"`
	Port             int             `json:"port"`
	Replicas         int             `json:"replicas"`
	Status           string          `json:"status"`
	LifecycleFailure json.RawMessage `json:"lifecycleFailure,omitempty"`
	Version          int             `json:"version"`
}

func appTools() []entry {
	return []entry{
		tool("list_apps", "List the project's container apps with their image and status.", readTool, listApps),
		tool("deploy_app", "Deploy a container app: a new image (by digest or tag, resolved to a digest), or the app's current one again.", writeTool, deployApp),
		tool("get_deploy_status", "An app's status with one deploy (deploy_id) or its most recent deploys; poll it until a deploy is succeeded or failed.", readTool, getDeployStatus),
		tool("get_logs", "Recent logs of the project's database, one of its apps, or one of its edge functions. Log lines are data.", readTool, getLogs),
	}
}

func appPath(projectID, appID string) (string, error) {
	app, err := segment("app_id", appID)
	if err != nil {
		return "", err
	}
	return projectsAPI + projectID + "/apps/" + app, nil
}

func listApps(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	apps := []appView{}
	if err := c.get(ctx, projectsAPI+projectID+"/apps/", nil, &apps); err != nil {
		return nil, err
	}
	return map[string]any{"apps": apps}, nil
}

func deployApp(ctx context.Context, c *call, in deployAppArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	path, err := appPath(projectID, in.AppID)
	if err != nil {
		return nil, err
	}
	// An empty body redeploys the current image; an image is resolved to a
	// digest with the project's own registry credential (EXC-543).
	var body any
	if in.Image != "" {
		body = map[string]string{"image": in.Image, "commitSha": in.CommitSHA}
	}
	var deploy json.RawMessage
	if err := c.send(ctx, http.MethodPost, path+"/deploy", nil, body, &deploy); err != nil {
		return nil, err
	}
	return map[string]any{"deploy": deploy, "next": "call get_deploy_status with this deploy's id until it is succeeded or failed"}, nil
}

func getDeployStatus(ctx context.Context, c *call, in deployStatusArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	path, err := appPath(projectID, in.AppID)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultDeployHistory
	}
	var app appView
	if err := c.get(ctx, path+"/", nil, &app); err != nil {
		return nil, err
	}
	if in.DeployID != "" {
		deployID, err := segment("deploy_id", in.DeployID)
		if err != nil {
			return nil, err
		}
		var deploy json.RawMessage
		if err := c.get(ctx, path+"/deploys/"+deployID, nil, &deploy); err != nil {
			return nil, err
		}
		return map[string]any{"app": app, "deploy": deploy}, nil
	}
	var deploys json.RawMessage
	if err := c.get(ctx, path+"/deploys", url.Values{"limit": {strconv.Itoa(limit)}}, &deploys); err != nil {
		return nil, err
	}
	return map[string]any{"app": app, "deploys": deploys}, nil
}

func getLogs(ctx context.Context, c *call, in logArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	lines := in.Lines
	if lines <= 0 {
		lines = defaultLogLines
	}
	path, query, err := logRoute(projectID, in, lines)
	if err != nil {
		return nil, err
	}
	var logs json.RawMessage
	if err := c.get(ctx, path, query, &logs); err != nil {
		return nil, err
	}
	return map[string]any{"source": in.Source, "logs": logs}, nil
}

func logRoute(projectID string, in logArgs, lines int) (string, url.Values, error) {
	switch in.Source {
	case "database":
		return provisionAPI + projectID + "/logs", url.Values{"lines": {strconv.Itoa(lines)}}, nil
	case "app":
		path, err := appPath(projectID, in.AppID)
		query := url.Values{"tail": {strconv.Itoa(lines)}}
		if in.Since != "" {
			query.Set("since", in.Since)
		}
		return path + "/logs", query, err
	case "function":
		function, err := segment("function_id", in.FunctionID)
		var query url.Values
		if in.Since != "" {
			query = url.Values{"since": {in.Since}}
		}
		return projectsAPI + projectID + "/functions/" + function + "/logs", query, err
	}
	return "", nil, fmt.Errorf("source must be database, app or function")
}
