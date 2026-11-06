package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// errAppSignInFirst: the GitHub sign-in is bound to the browser that starts it, so it never starts here.
var errAppSignInFirst = errors.New("the user has to sign in with GitHub first, so the platform sees which repositories they may read; " +
	"that sign-in starts in Studio and never through MCP, because it is bound to the browser that starts it. Ask them to do it, then call connect_app_repo again")

type connectAppRepoArgs struct {
	appArgs
	Repository     string            `json:"repository" jsonschema:"the GitHub repository as owner/name"`
	Branch         string            `json:"branch,omitempty" jsonschema:"the branch whose pushes build; the repository's default branch when left out"`
	Builder        string            `json:"builder,omitempty" jsonschema:"auto (default: a Dockerfile if the repository has one, else buildpacks), dockerfile or buildpacks"`
	RootDir        string            `json:"root_dir,omitempty" jsonschema:"the build context inside the repository, for a monorepo; left out keeps the current one, or the repository root on a first connect"`
	DockerfilePath string            `json:"dockerfile_path,omitempty" jsonschema:"the Dockerfile, relative to root_dir; left out keeps the current one, or Dockerfile on a first connect"`
	BuildArgs      map[string]string `json:"build_args,omitempty" jsonschema:"plain build arguments; they end up in the image's history, so never put a secret here. Left out keeps the current ones"`
}

type listAppBuildsArgs struct {
	appArgs
	BuildID string `json:"build_id,omitempty" jsonschema:"one build's id, to read it with its log; logs need Developer or above"`
}

// appGitTools are dark behind appgitbuilds (EXC-547). The GitHub sign-in and the choice of repository access stay in Studio.
func appGitTools() []entry {
	return []entry{
		dark(features.AppGitBuilds, tool("connect_app_repo", "Connect a GitHub repository and branch to a container app, so every push to that branch builds an image in a sandbox and deploys it; "+
			"the first build starts at once. The user must have signed in with GitHub and installed the platform's GitHub App; if not, this says what to ask them. "+
			"Build arguments are plain values in the image's history: never pass secrets (set_app_secret is for those).", writeTool, connectAppRepo)),
		dark(features.AppGitBuilds, tool("disconnect_app_repo", "Stop building a container app from git: pushes no longer build it and its queued builds are cancelled. "+
			"Needs Developer. The app keeps running its last deploy.", writeTool, disconnectAppRepo)),
		dark(features.AppGitBuilds, tool("rebuild_app", "Queue a build of the connected branch's latest commit now, without a push. Needs Developer; it counts toward the plan's app builds per hour. "+
			"Follow it with list_app_builds.", writeTool, rebuildApp)),
		dark(features.AppGitBuilds, tool("list_app_builds", "A container app's git builds, newest first, with their status and why one failed. "+
			"Pass build_id to read one build with its log (Developer and above).", readTool, listAppBuilds)),
	}
}

type appConnectionView struct {
	Repository     string   `json:"repository"`
	Branch         string   `json:"branch"`
	Builder        string   `json:"builder"`
	RootDir        string   `json:"rootDir"`
	DockerfilePath string   `json:"dockerfilePath"`
	BuildArgNames  []string `json:"buildArgNames"`
	InactiveReason string   `json:"inactiveReason,omitempty"`
}

// appConnectionAnswer reads the route's connection into a view; the build arguments are named, never shown.
type appConnectionAnswer struct {
	Repository     string `json:"repository"`
	InactiveReason string `json:"inactiveReason"`
	Settings       struct {
		Branch         string            `json:"branch"`
		Builder        string            `json:"builder"`
		RootDir        string            `json:"rootDir"`
		DockerfilePath string            `json:"dockerfilePath"`
		BuildArgs      map[string]string `json:"buildArgs"`
	} `json:"settings"`
}

func (a appConnectionAnswer) view() appConnectionView {
	names := make([]string, 0, len(a.Settings.BuildArgs))
	for name := range a.Settings.BuildArgs {
		names = append(names, name)
	}
	sort.Strings(names)
	return appConnectionView{Repository: a.Repository, Branch: a.Settings.Branch, Builder: a.Settings.Builder, RootDir: a.Settings.RootDir,
		DockerfilePath: a.Settings.DockerfilePath, BuildArgNames: names, InactiveReason: a.InactiveReason}
}

func connectAppRepo(ctx context.Context, c *call, in connectAppRepoArgs) (any, error) {
	path, err := appGitPath(c, in.appArgs)
	if err != nil {
		return nil, err
	}
	if in.Repository == "" {
		return nil, errors.New("repository is required, as owner/name")
	}
	buildArgs, rootDir, dockerfilePath := in.BuildArgs, in.RootDir, in.DockerfilePath
	if buildArgs == nil || rootDir == "" || dockerfilePath == "" {
		// A reconnect keeps what the caller leaves out, so it never wipes a monorepo's folder or Dockerfile.
		var current struct {
			Connection *appConnectionAnswer `json:"connection"`
		}
		if err := c.get(ctx, path+"/git", nil, &current); err != nil {
			return nil, siteBuildFailure(err)
		}
		if current.Connection != nil {
			settings := current.Connection.Settings
			if buildArgs == nil {
				buildArgs = settings.BuildArgs
			}
			rootDir, dockerfilePath = defaultString(rootDir, settings.RootDir), defaultString(dockerfilePath, settings.DockerfilePath)
		}
	}
	body := map[string]any{"repository": in.Repository, "settings": map[string]any{
		"branch": in.Branch, "builder": in.Builder, "rootDir": rootDir, "dockerfilePath": dockerfilePath, "buildArgs": buildArgs,
	}}
	var result struct {
		Connection *appConnectionAnswer `json:"connection"`
		Build      *buildView           `json:"build"`
		BuildError string               `json:"buildError"`
	}
	err = c.send(ctx, http.MethodPut, path+"/git", nil, body, &result)
	if isStatus(err, http.StatusForbidden) && strings.Contains(err.Error(), "sign in to GitHub") {
		return nil, errAppSignInFirst
	}
	if err != nil {
		return nil, siteBuildFailure(err)
	}
	out := map[string]any{"next": "each push to the branch now builds and deploys the app; follow a build with list_app_builds"}
	if result.Connection != nil {
		out["connection"] = result.Connection.view()
	}
	if result.Build != nil {
		out["build"] = result.Build
	}
	if result.BuildError != "" {
		out["buildError"] = result.BuildError
	}
	return out, nil
}

func appGitPath(c *call, in appArgs) (string, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return "", err
	}
	return appPath(projectID, in.AppID)
}

func disconnectAppRepo(ctx context.Context, c *call, in appArgs) (any, error) {
	path, err := appGitPath(c, in)
	if err != nil {
		return nil, err
	}
	if err := c.send(ctx, http.MethodDelete, path+"/git", nil, nil, nil); err != nil {
		return nil, siteBuildFailure(err)
	}
	return map[string]any{"disconnected": in.AppID, "next": "connect_app_repo connects a repository again"}, nil
}

func rebuildApp(ctx context.Context, c *call, in appArgs) (any, error) {
	path, err := appGitPath(c, in)
	if err != nil {
		return nil, err
	}
	var build buildView
	if err := c.send(ctx, http.MethodPost, path+"/builds", nil, nil, &build); err != nil {
		return nil, siteBuildFailure(err)
	}
	return map[string]any{"build": build, "next": "follow it with list_app_builds"}, nil
}

func listAppBuilds(ctx context.Context, c *call, in listAppBuildsArgs) (any, error) {
	path, err := appGitPath(c, in.appArgs)
	if err != nil {
		return nil, err
	}
	if in.BuildID != "" {
		buildID, err := segment("build_id", in.BuildID)
		if err != nil {
			return nil, err
		}
		var build buildLogView
		if err := c.get(ctx, path+"/builds/"+buildID, nil, &build); err != nil {
			return nil, siteBuildFailure(err)
		}
		return map[string]any{"build": build}, nil
	}
	builds := []buildView{}
	if err := c.get(ctx, path+"/builds", nil, &builds); err != nil {
		return nil, siteBuildFailure(err)
	}
	return map[string]any{"builds": builds}, nil
}
