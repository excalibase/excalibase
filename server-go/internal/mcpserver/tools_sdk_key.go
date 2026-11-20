package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

type revokeKeyArgs struct {
	projectArg
	KeyID int64 `json:"key_id" jsonschema:"the key's id, from get_project_info (publishableKeys)"`
}

func sdkKeyTools() []entry {
	return []entry{
		tool("revoke_publishable_key", "Revoke one publishable SDK key: an app that ships it stops signing in with it at once. Needs Developer. "+
			"A secret key is never revoked here; that is done in Studio.", writeTool, revokePublishableKey),
		tool("list_templates", "The built-in app templates (and, while the service catalog is on, its services) with what each would use of this project's plan "+
			"(fit, and refusals when it cannot be deployed now). Read-only: deploying one hands back generated credentials once, so that is done in Studio.", readTool, listTemplates),
	}
}

func revokePublishableKey(ctx context.Context, c *call, in revokeKeyArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	if in.KeyID <= 0 {
		return nil, fmt.Errorf("key_id is required")
	}
	keys, err := publishableKeys(ctx, c, projectID)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		if key.ID != in.KeyID {
			continue
		}
		path := projectsAPI + projectID + "/sdk-keys/" + strconv.FormatInt(key.ID, 10)
		if err := c.send(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
			return nil, err
		}
		return map[string]any{"revoked": key.ID, "keyPrefix": key.KeyPrefix, "name": key.Name}, nil
	}
	return nil, fmt.Errorf("no publishable key %d in this project: get_project_info lists them, and a secret key is revoked in Studio", in.KeyID)
}

type templateAppView struct {
	Name          string `json:"name"`
	Image         string `json:"image"`
	Internal      bool   `json:"internal"`
	Port          int    `json:"port,omitempty"`
	InternalPorts []int  `json:"internalPorts,omitempty"`
	Replicas      int    `json:"replicas"`
	MinMemory     string `json:"minMemory,omitempty"`
}

type templateFitView struct {
	Plan                    string   `json:"plan"`
	AppsNeeded              int      `json:"appsNeeded"`
	AppsHeld                int      `json:"appsHeld"`
	AppsAllowed             int      `json:"appsAllowed"`
	DiskBytes               int64    `json:"diskBytes"`
	DiskCapBytes            int64    `json:"diskCapBytes"`
	AppCPU                  string   `json:"appCpu"`
	AppMemory               string   `json:"appMemory"`
	PrivateNetworkOn        bool     `json:"privateNetworkOn"`
	CanTurnOnPrivateNetwork bool     `json:"canTurnOnPrivateNetwork"`
	DatabaseReady           bool     `json:"databaseReady"`
	Refusals                []string `json:"refusals"`
}

type templateView struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Summary             string            `json:"summary"`
	Category            string            `json:"category,omitempty"`
	License             string            `json:"license,omitempty"`
	Catalog             bool              `json:"catalog"`
	Apps                []templateAppView `json:"apps"`
	NeedsPrivateNetwork bool              `json:"needsPrivateNetwork"`
	NeedsDatabase       bool              `json:"needsDatabase"`
	Fit                 templateFitView   `json:"fit"`
}

func listTemplates(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	templates := []templateView{}
	if err := c.get(ctx, projectsAPI+projectID+"/app-templates/", nil, &templates); err != nil {
		return nil, err
	}
	return map[string]any{"templates": templates}, nil
}
