package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type setAppSecretArgs struct {
	appArgs
	Name  string `json:"name" jsonschema:"the variable's name, e.g. API_KEY"`
	Value string `json:"value" jsonschema:"the secret's value; it is stored in the project's vault and never returned"`
}

type redeployAppArgs struct {
	appArgs
	DeployID string `json:"deploy_id" jsonschema:"the earlier deploy to roll out again (get_deploy_status lists them)"`
}

type appDomainArgs struct {
	appArgs
	DomainID string `json:"domain_id" jsonschema:"the custom domain's id (list_app_domains or add_app_domain)"`
}

type addAppDomainArgs struct {
	appArgs
	Hostname string `json:"hostname" jsonschema:"the user's own hostname, such as www.example.com; not a bare registered domain, an address, or a platform domain"`
}

const appSecretWarning = "WARNING: the value passes through this conversation and the AI tool's logs. Prefer asking the user to set it in Studio " +
	"(Containers, the app's form); use this only for a test value or when the user explicitly asks."

const appDomainsUnavailableMessage = "custom domains are not set up on this install; the app keeps serving at its own hostname (list_apps)"

func appMoreTools() []entry {
	return []entry{
		tool("set_app_secret", "Set one secret environment variable of a container app: the value goes to the project's vault and the variable points at it. "+
			"No tool returns the value; the running app reads it after its next deploy_app, and an image that prints its environment can still write it to the app's logs (get_logs). "+
			"It replaces another secret of the same name, but refuses a literal variable or a database reference of that name (remove those with update_app remove_env first). "+
			"That guard stops accidents, not a misled agent: update_app set_env replaces a variable in one call. "+
			appSecretWarning, writeTool, setAppSecret),
		tool("redeploy_app", "Roll an earlier deploy of a container app out again as a new deploy, with the config that deploy froze; it does not edit the app. "+
			"Use it to go back to a version that worked. Needs Developer. Follow it with get_deploy_status.", writeTool, redeployApp),
		tool("list_app_domains", "A container app's custom domains with their status (pending, issuing, active, issue_failed or detached) and the CNAME target each must point at.", readTool, listAppDomains),
		tool("add_app_domain", "Add the user's own domain to a container app, such as www.example.com (a subdomain: a bare example.com cannot point a CNAME). "+
			"Nothing is routed until the user adds a CNAME to the answer's cnameTarget at their DNS provider and verify_app_domain sees it. At most 5 domains per app.", writeTool, addAppDomain),
		tool("verify_app_domain", "Check a custom domain now: it is routed only if its CNAME names the app's own hostname; its certificate is then issued. "+
			"DNS changes can take minutes to hours to be seen.", writeTool, verifyAppDomain),
		tool("remove_app_domain", "Remove a custom domain from a container app: it stops being routed.", writeTool, removeAppDomain),
	}
}

// deployResultView is what an agent sees of a deploy: its state and image. The
// frozen spec, with its environment, and who started it stay out.
type deployResultView struct {
	ID            string     `json:"id"`
	AppID         string     `json:"appId"`
	Revision      int        `json:"revision"`
	Image         string     `json:"image"`
	Digest        string     `json:"digest,omitempty"`
	Source        string     `json:"source,omitempty"`
	CommitSHA     string     `json:"commitSha,omitempty"`
	RedeployOf    string     `json:"redeployOf,omitempty"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	FailureReason string     `json:"failureReason,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	URL           string     `json:"url,omitempty"`
}

type appDomainView struct {
	ID            string     `json:"id"`
	Hostname      string     `json:"hostname"`
	Status        string     `json:"status"`
	FailureReason string     `json:"failureReason,omitempty"`
	CNAMETarget   string     `json:"cnameTarget"`
	VerifiedAt    *time.Time `json:"verifiedAt,omitempty"`
	LastCheckedAt *time.Time `json:"lastCheckedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

func setAppSecret(ctx context.Context, c *call, in setAppSecretArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	path, err := appPath(projectID, in.AppID)
	if err != nil {
		return nil, err
	}
	name, err := segment("name", in.Name)
	if err != nil {
		return nil, err
	}
	if in.Value == "" {
		return nil, errors.New("value is required")
	}
	replaced, err := existingSecret(ctx, c, path, in.Name)
	if err != nil {
		return nil, err
	}
	if err := c.send(ctx, http.MethodPut, path+"/secrets/"+name, nil, map[string]string{"value": in.Value}, nil); err != nil {
		return nil, err
	}
	return map[string]any{"name": in.Name, "set": true, "replaced": replaced, "next": "the running app reads it after its next deploy_app"}, nil
}

func redeployApp(ctx context.Context, c *call, in redeployAppArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	path, err := appPath(projectID, in.AppID)
	if err != nil {
		return nil, err
	}
	deployID, err := segment("deploy_id", in.DeployID)
	if err != nil {
		return nil, err
	}
	var raw struct {
		deployResultView
		Spec struct {
			URL string `json:"url"`
		} `json:"spec"`
	}
	if err := c.send(ctx, http.MethodPost, path+"/deploys/"+deployID+"/redeploy", nil, nil, &raw); err != nil {
		return nil, err
	}
	raw.URL = raw.Spec.URL
	return map[string]any{"deploy": raw.deployResultView, "next": "call get_deploy_status with this deploy's id until it is succeeded or failed"}, nil
}

func appDomainsPath(c *call, in appArgs) (string, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return "", err
	}
	path, err := appPath(projectID, in.AppID)
	if err != nil {
		return "", err
	}
	return path + "/domains/", nil
}

// appDomainFailure says plainly when this install has no custom domains for apps.
func appDomainFailure(err error) error {
	if isStatus(err, http.StatusNotFound) && strings.Contains(err.Error(), "not available on this platform") {
		return errors.New(appDomainsUnavailableMessage)
	}
	return err
}

func listAppDomains(ctx context.Context, c *call, in appArgs) (any, error) {
	path, err := appDomainsPath(c, in)
	if err != nil {
		return nil, err
	}
	domains := []appDomainView{}
	if err := c.get(ctx, path, nil, &domains); err != nil {
		return nil, appDomainFailure(err)
	}
	return map[string]any{"domains": domains}, nil
}

func addAppDomain(ctx context.Context, c *call, in addAppDomainArgs) (any, error) {
	path, err := appDomainsPath(c, in.appArgs)
	if err != nil {
		return nil, err
	}
	var added appDomainView
	if err := c.send(ctx, http.MethodPost, path, nil, map[string]string{"hostname": in.Hostname}, &added); err != nil {
		return nil, appDomainFailure(err)
	}
	next := fmt.Sprintf("ask the user to add a CNAME record for %s pointing at %s at their DNS provider, then call verify_app_domain with domain_id %s until its status is active",
		added.Hostname, added.CNAMETarget, added.ID)
	return map[string]any{"domain": added, "next": next}, nil
}

func verifyAppDomain(ctx context.Context, c *call, in appDomainArgs) (any, error) {
	path, err := appDomainPath(c, in)
	if err != nil {
		return nil, err
	}
	var checked appDomainView
	if err := c.send(ctx, http.MethodPost, path+"/verify", nil, nil, &checked); err != nil {
		return nil, appDomainFailure(err)
	}
	return map[string]any{"domain": checked}, nil
}

func removeAppDomain(ctx context.Context, c *call, in appDomainArgs) (any, error) {
	path, err := appDomainPath(c, in)
	if err != nil {
		return nil, err
	}
	if err := c.send(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return nil, appDomainFailure(err)
	}
	return map[string]any{"removed": in.DomainID}, nil
}

func appDomainPath(c *call, in appDomainArgs) (string, error) {
	path, err := appDomainsPath(c, in.appArgs)
	if err != nil {
		return "", err
	}
	domainID, err := segment("domain_id", in.DomainID)
	if err != nil {
		return "", err
	}
	return path + domainID, nil
}

// existingSecret reports whether the app already has a secret of that name. A
// literal or a database reference of the name is refused: the write would
// replace it without the user having decided that.
func existingSecret(ctx context.Context, c *call, path, name string) (bool, error) {
	var app struct {
		Env []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"env"`
	}
	if err := c.get(ctx, path+"/", nil, &app); err != nil {
		return false, err
	}
	for _, variable := range app.Env {
		switch {
		case variable.Name != name:
		case variable.Kind == "secret":
			return true, nil
		default:
			return false, fmt.Errorf("%s is already a %s variable of this app; set_app_secret would replace it. Remove it first with update_app remove_env if that is what the user wants", name, variable.Kind)
		}
	}
	return false, nil
}
