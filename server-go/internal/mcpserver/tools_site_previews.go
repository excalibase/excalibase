package mcpserver

import (
	"context"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/features"
)

type setSitePreviewModeArgs struct {
	siteArgs
	Mode string `json:"mode" jsonschema:"off, pull-requests (one preview per open pull request) or branches (one per branch other than the production one)"`
}

type removeSitePreviewArgs struct {
	siteArgs
	PreviewID string `json:"preview_id" jsonschema:"the preview's id (list_site_previews)"`
}

// sitePreviewTools are dark behind sitepreviews (EXC-660). The opt-ins that reach past a
// preview (forks, secrets, API access, public links) are an Admin's choice in Studio, never a tool.
func sitePreviewTools() []entry {
	return []entry{
		dark(features.SitePreviews, tool("list_site_previews", "A git-connected site's previews: one per open pull request or branch, each at its own "+
			"unguessable link, with its status and commit, the preview mode and how many of the plan's previews the project uses.",
			readTool, listSitePreviews)),
		dark(features.SitePreviews, tool("set_site_preview_mode", "Choose which pushes get a preview of a git-connected site: off, pull-requests or "+
			"branches. off removes the site's previews. Previews get no project secrets and cannot call the project's API unless an Admin allows it in Studio.",
			writeTool, setSitePreviewMode)),
		dark(features.SitePreviews, tool("remove_site_preview", "Take one preview down now: its link stops working and its files are deleted. "+
			"A later push to the branch or pull request makes it again.", writeTool, removeSitePreview)),
	}
}

func listSitePreviews(ctx context.Context, c *call, in siteArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	var overview map[string]any
	if err := c.get(ctx, path+"/previews", nil, &overview); err != nil {
		return nil, siteBuildFailure(err)
	}
	return overview, nil
}

func setSitePreviewMode(ctx context.Context, c *call, in setSitePreviewModeArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	var overview map[string]any
	if err := c.send(ctx, http.MethodPut, path+"/previews/mode", nil, map[string]string{"mode": in.Mode}, &overview); err != nil {
		return nil, siteBuildFailure(err)
	}
	return overview, nil
}

func removeSitePreview(ctx context.Context, c *call, in removeSitePreviewArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	previewID, err := segment("preview_id", in.PreviewID)
	if err != nil {
		return nil, err
	}
	if err := c.send(ctx, http.MethodDelete, path+"/previews/"+previewID, nil, nil, nil); err != nil {
		return nil, siteBuildFailure(err)
	}
	return map[string]any{"removed": previewID}, nil
}
