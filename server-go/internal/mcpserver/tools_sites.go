package mcpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// maxSiteArchiveBytes bounds a deploy_site archive so its base64 fits one MCP
// message; a bigger build goes up from CI or a shell with get_ci_snippet.
const maxSiteArchiveBytes = 1 << 20

const sitesUnavailableMessage = "sites are not available on this install: it has no Cloudflare account configured for them. " +
	"Host the frontend as a container instead: get_dockerfile_template (stack vite or static), then create_app and deploy_app"

const archiveHowTo = "pack the build output folder with `tar czf - -C dist . | base64` (dist being where the build writes)"

type siteArgs struct {
	projectArg
	SiteID string `json:"site_id" jsonschema:"the site's id (list_sites or create_site)"`
}

type createSiteArgs struct {
	projectArg
	Name        string `json:"name" jsonschema:"the site's name, its hostname's first label: 3 to 63 lowercase letters, digits or hyphens"`
	SPAFallback *bool  `json:"spa_fallback,omitempty" jsonschema:"true answers unknown paths with /index.html (single-page apps); false answers the nearest 404.html. true when left out"`
}

type deploySiteArgs struct {
	siteArgs
	Archive string `json:"archive_base64" jsonschema:"the build output folder as a gzipped tar, base64-encoded: tar czf - -C dist . | base64; at most 1 MiB before encoding"`
	Label   string `json:"label,omitempty" jsonschema:"the commit or ref the build came from, shown in the deploy history"`
}

type rollbackSiteArgs struct {
	siteArgs
	DeployID string `json:"deploy_id" jsonschema:"the earlier deploy to make live again (list_site_deploys)"`
}

func siteTools() []entry {
	return []entry{
		tool("list_sites", "List the project's sites (static frontends served from the CDN) with their URL and live deploy.", readTool, listSites),
		tool("create_site", "Create a site: a hostname that serves a frontend's built files (Vite, Astro static, Next export, SvelteKit static, plain HTML) from the CDN. "+
			"Its origin is added to the project's CORS allowlist on the first deploy. A server-rendered app (Next.js SSR, Nuxt, Remix) is a container instead: create_app.", writeTool, createSite),
		tool("deploy_site", "Deploy built files to a site and make them live. Build first, then pack the output folder as a base64 gzipped tar: "+
			"`tar czf - -C dist . | base64`, at most 1 MiB before encoding. A bigger build deploys from CI or a shell: get_ci_snippet with kind site. "+
			"The archive is checked like any upload (paths, file count and sizes) before anything is published.", writeTool, deploySite),
		tool("list_site_deploys", "A site's recent deploys, newest first; live marks the one it serves.", readTool, listSiteDeploys),
		tool("rollback_site", "Make an earlier succeeded deploy of a site live again, with the routing it was published with.", writeTool, rollbackSite),
		tool("delete_site", "Delete a site: its hostname stops serving at once and its deploy history is removed.", writeTool, deleteSite),
	}
}

func sitePath(c *call, projectArgument, siteID string) (string, error) {
	projectID, err := c.useProject(projectArgument)
	if err != nil {
		return "", err
	}
	site, err := segment("site_id", siteID)
	if err != nil {
		return "", err
	}
	return projectsAPI + projectID + "/sites/" + site, nil
}

// siteFailure says plainly when the install has no Sites, and where a frontend goes instead.
func siteFailure(err error) error {
	if isStatus(err, http.StatusServiceUnavailable) {
		return errors.New(sitesUnavailableMessage)
	}
	return err
}

func listSites(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var listed []map[string]any
	if err := c.get(ctx, projectsAPI+projectID+"/sites/", nil, &listed); err != nil {
		return nil, siteFailure(err)
	}
	if listed == nil {
		listed = []map[string]any{}
	}
	return map[string]any{"sites": listed}, nil
}

func createSite(ctx context.Context, c *call, in createSiteArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": in.Name}
	if in.SPAFallback != nil {
		body["spaFallback"] = *in.SPAFallback
	}
	var site map[string]any
	if err := c.send(ctx, http.MethodPost, projectsAPI+projectID+"/sites/", nil, body, &site); err != nil {
		return nil, siteFailure(err)
	}
	return map[string]any{"site": site, "next": "build the frontend, then call deploy_site with this site's id: " + archiveHowTo}, nil
}

func deploySite(ctx context.Context, c *call, in deploySiteArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	archive, err := decodeArchive(in.Archive)
	if err != nil {
		return nil, err
	}
	var query url.Values
	if in.Label != "" {
		query = url.Values{"label": {in.Label}}
	}
	var deploy map[string]any
	upload := request{method: http.MethodPost, path: path + "/deploy", query: query, raw: archive, contentType: "application/gzip"}
	if err := c.do(ctx, upload, &deploy); err != nil {
		return nil, siteFailure(err)
	}
	out := map[string]any{"deploy": deploy}
	// The deploy is live either way; the URL only saves the client a list_sites call.
	var site struct {
		URL string `json:"url"`
	}
	if err := c.get(ctx, path+"/", nil, &site); err != nil {
		log.Printf("mcp: read site after deploy %s: %v", path, err)
		return out, nil
	}
	out["url"] = site.URL
	return out, nil
}

// decodeArchive reads the base64 archive, ignoring the line breaks a terminal wraps it with.
func decodeArchive(encoded string) ([]byte, error) {
	compact := strings.Join(strings.Fields(encoded), "")
	if compact == "" {
		return nil, errors.New("archive_base64 is required: " + archiveHowTo)
	}
	if base64.StdEncoding.DecodedLen(len(compact)) > maxSiteArchiveBytes+2 {
		return nil, tooLargeForMCP()
	}
	archive, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		return nil, errors.New("archive_base64 is not base64: " + archiveHowTo)
	}
	if len(archive) > maxSiteArchiveBytes {
		return nil, tooLargeForMCP()
	}
	return archive, nil
}

func tooLargeForMCP() error {
	return fmt.Errorf("the archive is larger than the %d MiB a deploy through MCP carries. "+
		"Deploy it from CI or a shell instead: get_ci_snippet with kind site gives the pipeline, or a curl script (provider curl) to run with the site's deploy token (Studio, the site's Deploy tokens)", maxSiteArchiveBytes>>20)
}

func listSiteDeploys(ctx context.Context, c *call, in siteArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	var deploys []map[string]any
	if err := c.get(ctx, path+"/deploys", nil, &deploys); err != nil {
		return nil, siteFailure(err)
	}
	if deploys == nil {
		deploys = []map[string]any{}
	}
	return map[string]any{"deploys": deploys}, nil
}

func rollbackSite(ctx context.Context, c *call, in rollbackSiteArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	deployID, err := segment("deploy_id", in.DeployID)
	if err != nil {
		return nil, err
	}
	var site map[string]any
	if err := c.send(ctx, http.MethodPost, path+"/deploys/"+deployID+"/rollback", nil, nil, &site); err != nil {
		return nil, siteFailure(err)
	}
	return map[string]any{"site": site}, nil
}

func deleteSite(ctx context.Context, c *call, in siteArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	if err := c.send(ctx, http.MethodDelete, path+"/", nil, nil, nil); err != nil {
		return nil, siteFailure(err)
	}
	return map[string]any{"deleted": in.SiteID}, nil
}
