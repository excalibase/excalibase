package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

type envSetting struct {
	Name  string `json:"name" jsonschema:"the variable's name, e.g. LOG_LEVEL"`
	Value string `json:"value" jsonschema:"its literal value; secrets are never set through MCP (Studio keeps them out of the conversation)"`
}

type updateAppArgs struct {
	appArgs
	Image           string       `json:"image,omitempty" jsonschema:"a new image or tag to run; takes effect on the next deploy_app"`
	Port            int          `json:"port,omitempty" jsonschema:"the port the image serves HTTP on"`
	Replicas        *int         `json:"replicas,omitempty" jsonschema:"how many copies run, 0 to 3 within the plan; 0 stops the app"`
	HealthCheckPath string       `json:"health_check_path,omitempty" jsonschema:"a path that answers 200 when the app is ready"`
	SetEnv          []envSetting `json:"set_env,omitempty" jsonschema:"literal environment variables to add or replace by name; the others are kept"`
	RemoveEnv       []string     `json:"remove_env,omitempty" jsonschema:"names of environment variables to remove"`
	PrivateNetwork  *bool        `json:"private_network,omitempty" jsonschema:"let the project's apps reach each other by name; a project-wide switch that needs the admin role"`
}

func appManageTools() []entry {
	return []entry{
		tool("update_app", "Change a container app's image or tag, port, replicas (within the plan), health check path, environment variables, or the project's private network between apps. "+
			"Only what you pass changes; variables not named are kept and secrets stay as they are. A change reaches the running app on the next deploy_app.", writeTool, updateApp),
		tool("delete_app", "Delete one container app: it stops, and the CORS origin its create_app added goes with it. Other apps, the database and the rest of the CORS list are untouched. "+
			"An app that has a disk is refused: its data would be lost, so that one is deleted in Studio.", writeTool, deleteApp),
	}
}

// storedApp is the app as the route holds it, with the env the tool views hide.
type storedApp struct {
	appView
	Env []json.RawMessage `json:"env"`
}

func (in updateAppArgs) changesApp() bool {
	return in.Image != "" || in.Port != 0 || in.Replicas != nil || in.HealthCheckPath != "" || len(in.SetEnv) > 0 || len(in.RemoveEnv) > 0
}

func updateApp(ctx context.Context, c *call, in updateAppArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	path, err := appPath(projectID, in.AppID)
	if err != nil {
		return nil, err
	}
	if !in.changesApp() && in.PrivateNetwork == nil {
		return nil, errors.New("nothing to change: pass image, port, replicas, health_check_path, set_env, remove_env or private_network")
	}
	out := map[string]any{}
	if in.changesApp() {
		app, err := patchApp(ctx, c, path, in)
		if err != nil {
			return nil, err
		}
		out["app"] = app.appView
		out["next"] = "the change is stored; call deploy_app to run it"
	}
	if in.PrivateNetwork != nil {
		var network map[string]any
		body := map[string]bool{"privateNetwork": *in.PrivateNetwork}
		if err := c.send(ctx, http.MethodPut, projectsAPI+projectID+"/app-network/", nil, body, &network); err != nil {
			if out["app"] != nil {
				return nil, fmt.Errorf("the app's settings were stored, but the private network was not changed: %w", err)
			}
			return nil, err
		}
		out["privateNetwork"] = network["privateNetwork"]
	}
	return out, nil
}

// patchApp reads the app, builds a partial body, and sends it at the version
// it read, so a change made meanwhile is refused rather than overwritten.
func patchApp(ctx context.Context, c *call, path string, in updateAppArgs) (storedApp, error) {
	var current storedApp
	if err := c.get(ctx, path+"/", nil, &current); err != nil {
		return current, err
	}
	body := map[string]any{}
	if in.Image != "" {
		body["image"] = in.Image
	}
	if in.Port != 0 {
		body["port"] = in.Port
	}
	if in.Replicas != nil {
		body["replicas"] = *in.Replicas
	}
	if in.HealthCheckPath != "" {
		body["healthCheckPath"] = in.HealthCheckPath
	}
	if len(in.SetEnv) > 0 || len(in.RemoveEnv) > 0 {
		env, err := mergeEnv(current.Env, in.SetEnv, in.RemoveEnv)
		if err != nil {
			return current, err
		}
		body["env"] = env
	}
	header := http.Header{"If-Match": {strconv.Itoa(current.Version)}}
	var updated storedApp
	err := c.do(ctx, request{method: http.MethodPatch, path: path + "/", body: body, header: header}, &updated)
	return updated, err
}

// mergeEnv keeps every stored variable (secrets and references included, as
// the route holds them) except those removed or replaced, then adds the
// literals named.
func mergeEnv(stored []json.RawMessage, set []envSetting, remove []string) ([]json.RawMessage, error) {
	dropped := map[string]bool{}
	for _, name := range remove {
		dropped[name] = true
	}
	for _, variable := range set {
		if variable.Name == "" {
			return nil, errors.New("set_env needs a name for every variable")
		}
		dropped[variable.Name] = true
	}
	merged := make([]json.RawMessage, 0, len(stored)+len(set))
	for _, raw := range stored {
		var named struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &named); err != nil {
			return nil, errors.New("the app's stored environment could not be read")
		}
		if !dropped[named.Name] {
			merged = append(merged, raw)
		}
	}
	for _, variable := range set {
		literal, err := json.Marshal(map[string]string{"name": variable.Name, "kind": "literal", "value": variable.Value})
		if err != nil {
			return nil, err
		}
		merged = append(merged, literal)
	}
	return merged, nil
}

func deleteApp(ctx context.Context, c *call, in appArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	path, err := appPath(projectID, in.AppID)
	if err != nil {
		return nil, err
	}
	if err := c.send(ctx, http.MethodDelete, path+"/", nil, nil, nil); err != nil {
		return nil, err
	}
	return map[string]any{
		"deleted": in.AppID,
		"note":    "Only this app was deleted, with the CORS origin it added; list_cors_origins shows what is left (changes reach the data plane within about a minute).",
	}, nil
}
