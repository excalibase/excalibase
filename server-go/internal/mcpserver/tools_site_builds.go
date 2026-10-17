package mcpserver

import (
	"context"
	"errors"
	"net/http"
)

const siteBuildsUnavailableMessage = "git builds are not available on this install. Build the site yourself and deploy the output: " +
	"deploy_site for a small build, or get_ci_snippet with kind site for CI"

type connectSiteRepoArgs struct {
	siteArgs
	Repository     string            `json:"repository" jsonschema:"the GitHub repository as owner/name"`
	Branch         string            `json:"branch,omitempty" jsonschema:"the branch whose pushes deploy; the repository's default branch when left out"`
	RootDir        string            `json:"root_dir,omitempty" jsonschema:"the folder of the frontend inside the repository, for a monorepo; the repository root when left out"`
	InstallCommand string            `json:"install_command,omitempty" jsonschema:"left out, the lockfile decides: npm ci, pnpm install --frozen-lockfile or yarn install"`
	BuildCommand   string            `json:"build_command,omitempty" jsonschema:"npm run build when left out"`
	OutputDir      string            `json:"output_dir,omitempty" jsonschema:"where the build writes its files, relative to root_dir: dist (Vite), build, out (Next export); dist when left out"`
	NodeVersion    string            `json:"node_version,omitempty" jsonschema:"the Node major: 20, 22 or 24; 22 when left out"`
	BuildEnv       map[string]string `json:"build_env,omitempty" jsonschema:"plain build variables such as VITE_API_URL; they end up in the built files, so never put a secret here. Left out keeps the current ones"`
}

// gitState is the part of a site's git connection a reconnect keeps: its build variables.
type gitState struct {
	Connection *struct {
		Settings gitSettings `json:"settings"`
	} `json:"connection"`
}

type gitSettings struct {
	Env map[string]string `json:"env"`
}

type listSiteBuildsArgs struct {
	siteArgs
	BuildID string `json:"build_id,omitempty" jsonschema:"one build's id, to read it with its log; logs need Developer or above"`
}

func siteBuildTools() []entry {
	return []entry{
		tool("connect_site_repo", "Connect a GitHub repository and branch to a site, so every push to that branch builds the site in a sandbox and deploys it; "+
			"the first build starts at once. The user must have installed the platform's GitHub App and signed in with GitHub from Studio; "+
			"if not, this says how they do that in Studio, then call it again. Build variables are public in the built files: never pass secrets.",
			writeTool, connectSiteRepo),
		tool("list_site_builds", "A site's git builds, newest first, with their status and why one failed (out of memory, time limit, the build command). "+
			"Pass build_id to read one build with its log (Developer and above).", readTool, listSiteBuilds),
	}
}

// siteBuildFailure says plainly when the install offers no git builds (single-host answers 503 with its reason).
func siteBuildFailure(err error) error {
	var routeErr *RouteError
	switch {
	case isStatus(err, http.StatusServiceUnavailable) && errors.As(err, &routeErr) && routeErr.Message != "":
		return errors.New(routeErr.Message)
	case isStatus(err, http.StatusNotFound):
		return errors.New("not found: the site or build does not exist, or " + siteBuildsUnavailableMessage)
	}
	return err
}

func connectSiteRepo(ctx context.Context, c *call, in connectSiteRepoArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	if in.Repository == "" {
		return nil, errors.New("repository is required, as owner/name")
	}
	env := in.BuildEnv
	if env == nil {
		var current gitState
		if err := c.get(ctx, path+"/git", nil, &current); err != nil {
			return nil, siteBuildFailure(err)
		}
		if current.Connection != nil {
			env = current.Connection.Settings.Env
		}
	}
	body := map[string]any{"repository": in.Repository, "settings": map[string]any{
		"branch": in.Branch, "rootDir": in.RootDir, "installCommand": in.InstallCommand, "buildCommand": in.BuildCommand,
		"outputDir": defaultString(in.OutputDir, "dist"), "nodeVersion": in.NodeVersion, "env": env,
	}}
	var result map[string]any
	err = c.send(ctx, http.MethodPut, path+"/git", nil, body, &result)
	if isStatus(err, http.StatusForbidden) {
		return nil, errSignInFirst
	}
	if err != nil {
		return nil, siteBuildFailure(err)
	}
	result["next"] = "each push to the branch now builds and deploys the site; follow a build with list_site_builds"
	return result, nil
}

// errSignInFirst: the sign-in is bound to the browser that starts it, so it starts in Studio, never from a link handed over.
var errSignInFirst = errors.New("the user has to sign in with GitHub first, so the platform sees which repositories they may read: " +
	"ask them to open the site in Studio (Sites, then the site) and use Sign in with GitHub under Deploy on git push, " +
	"installing the GitHub App on the repository if it is not listed; then call connect_site_repo again within the hour")

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func listSiteBuilds(ctx context.Context, c *call, in listSiteBuildsArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	if in.BuildID != "" {
		buildID, err := segment("build_id", in.BuildID)
		if err != nil {
			return nil, err
		}
		var build map[string]any
		if err := c.get(ctx, path+"/builds/"+buildID, nil, &build); err != nil {
			return nil, siteBuildFailure(err)
		}
		return map[string]any{"build": build}, nil
	}
	var builds []map[string]any
	if err := c.get(ctx, path+"/builds", nil, &builds); err != nil {
		return nil, siteBuildFailure(err)
	}
	if builds == nil {
		builds = []map[string]any{}
	}
	return map[string]any{"builds": builds}, nil
}
