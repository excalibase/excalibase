package mcpserver

import (
	"context"
	"net/http"
	"time"
)

func siteGitTools() []entry {
	return []entry{
		tool("disconnect_site_repo", "Stop building a site from git: pushes no longer build it, its queued builds are cancelled and its previews are removed. "+
			"Needs Developer. Connect again with connect_site_repo.", writeTool, disconnectSiteRepo),
		tool("rebuild_site", "Queue a build of the connected branch's latest commit now, without a push. Needs Developer, and it counts toward the project's deploys per hour. "+
			"Follow it with list_site_builds.", writeTool, rebuildSite),
		tool("list_site_build_secrets", "The names of the project's secrets and whether a site's git build can read each one (buildVisible). "+
			"Values are never returned. Needs Developer. Choosing which secrets builds see is done in Studio, because a visible secret ends up in the built files.",
			readTool, listSiteBuildSecrets),
	}
}

// buildView is what an agent sees of a build: its state and commit. The
// settings, the log and who asked stay out; list_site_builds with a build_id has the log.
type buildView struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	Trigger       string     `json:"trigger"`
	CommitSHA     string     `json:"commitSha"`
	CommitMessage string     `json:"commitMessage,omitempty"`
	Repository    string     `json:"repository"`
	DeployID      string     `json:"deployId,omitempty"`
	FailureKind   string     `json:"failureKind,omitempty"`
	FailureReason string     `json:"failureReason,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
}

// buildLogView is one build read with its log, which build-visible secrets are redacted from.
type buildLogView struct {
	buildView
	Log string `json:"log,omitempty"`
}

type secretVisibilityView struct {
	Name         string `json:"name"`
	BuildVisible bool   `json:"buildVisible"`
}

func disconnectSiteRepo(ctx context.Context, c *call, in siteArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	if err := c.send(ctx, http.MethodDelete, path+"/git", nil, nil, nil); err != nil {
		return nil, siteBuildFailure(err)
	}
	return map[string]any{"disconnected": in.SiteID, "next": "connect_site_repo connects a repository again"}, nil
}

func rebuildSite(ctx context.Context, c *call, in siteArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	var build buildView
	if err := c.send(ctx, http.MethodPost, path+"/builds", nil, nil, &build); err != nil {
		return nil, siteBuildFailure(err)
	}
	return map[string]any{"build": build, "next": "follow it with list_site_builds"}, nil
}

func listSiteBuildSecrets(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var listed []secretVisibilityView
	if err := c.get(ctx, projectsAPI+projectID+sitesSegment+"build-secrets", nil, &listed); err != nil {
		return nil, siteBuildFailure(err)
	}
	if listed == nil {
		listed = []secretVisibilityView{}
	}
	return map[string]any{"secrets": listed}, nil
}
