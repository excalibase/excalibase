package mcpserver

import (
	"context"
	"errors"

	"github.com/excalibase/provisioning-poc/internal/shiptemplates"
)

type dockerfileArgs struct {
	Stack   string `json:"stack,omitempty" jsonschema:"node, nestjs, nextjs, vite, static, python, go or java; leave it out when unsure to get what to look for and what to ask"`
	Variant string `json:"variant,omitempty" jsonschema:"node, nestjs, nextjs, vite: npm or pnpm; python: gunicorn or uvicorn; java: spring-boot, spring-boot-gradle or quarkus"`
}

type ciArgs struct {
	projectArg
	Kind       string `json:"kind,omitempty" jsonschema:"app (a container image, the default) or site (built files uploaded to a site)"`
	Provider   string `json:"provider" jsonschema:"github-actions, gitlab-ci, jenkins or curl"`
	AppID      string `json:"app_id,omitempty" jsonschema:"the app the pipeline deploys (list_apps); its image names the repository CI pushes to"`
	Image      string `json:"image,omitempty" jsonschema:"the registry repository CI pushes to, e.g. ghcr.io/team/app; the app's own image when left out"`
	Context    string `json:"context,omitempty" jsonschema:"the build folder inside the repository, e.g. apps/web for a monorepo; the repository root when left out"`
	Dockerfile string `json:"dockerfile,omitempty" jsonschema:"the Dockerfile's path from the repository root, e.g. apps/web/Dockerfile; the context's Dockerfile when left out"`
	Branch     string `json:"branch,omitempty" jsonschema:"the branch whose pushes deploy; main (GitHub) or the default branch (GitLab) when left out. GitHub tags the image with it and the commit sha"`
	SiteID     string `json:"site_id,omitempty" jsonschema:"kind site: the site the pipeline deploys to (list_sites)"`
	Output     string `json:"output,omitempty" jsonschema:"kind site: the build output folder inside the build context; dist when left out"`
}

func shippingTools() []entry {
	return []entry{
		tool("get_dockerfile_template", "A Dockerfile and .dockerignore for a stack, ready to write into the repository. Every image serves on port 8080 (PORT), "+
			"runs as a non-root user and names its health check path. When the stack is not clear from the repository, call it without a stack: "+
			"it answers what to look for and what to ask the user, instead of guessing.", readTool, getDockerfileTemplate),
		tool("get_ci_snippet", "A CI pipeline (GitHub Actions, GitLab CI, Jenkins, or a curl step for any other CI) that builds the image, pushes it to your registry and deploys it to an app by digest. The same pipeline Studio's pipeline page shows. Call it after the app exists (create_app, list_apps). "+
			"GitHub deploys with the excalibase/deploy-action action; the others with one curl that waits until the deploy is live. For a monorepo pass context and dockerfile; deploys run one at a time. "+
			"With kind site it is the pipeline for a site instead: npm ci and npm run build in context, tar czf of the output folder, and one curl that uploads it to the site, live when it answers. "+
			"A site's pipeline authenticates with the site's deploy token in the EXCALIBASE_DEPLOY_TOKEN secret, made by a Developer in Studio under the site's Deploy tokens: it can only deploy that site.", readTool, getCISnippet),
	}
}

func getDockerfileTemplate(_ context.Context, _ *call, in dockerfileArgs) (any, error) {
	if in.Stack == "" {
		return shiptemplates.Questions(), nil
	}
	return shiptemplates.Dockerfile(in.Stack, in.Variant)
}

func getCISnippet(ctx context.Context, c *call, in ciArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	switch in.Kind {
	case "", "app":
		return appCISnippet(ctx, c, projectID, in)
	case "site":
		return siteCISnippet(ctx, c, projectID, in)
	}
	return nil, errors.New("kind must be app or site")
}

func appCISnippet(ctx context.Context, c *call, projectID string, in ciArgs) (any, error) {
	image := in.Image
	if image == "" && in.AppID != "" {
		path, err := appPath(projectID, in.AppID)
		if err != nil {
			return nil, err
		}
		var app appView
		if err := c.get(ctx, path+"/", nil, &app); err != nil {
			return nil, err
		}
		image = app.Image
	}
	if image == "" {
		return nil, errors.New("name the image (the registry repository CI pushes to) or an app whose image names it")
	}
	target := shiptemplates.DeployTarget{
		APIBase: c.settings.StudioURL, ProjectID: projectID, AppID: in.AppID, Image: image,
		Build: shiptemplates.BuildOptions{Context: in.Context, Dockerfile: in.Dockerfile, Branch: in.Branch},
	}
	return shiptemplates.CI(in.Provider, target)
}

// siteCISnippet reads the named site first, so a missing site or an install without Sites says so here.
func siteCISnippet(ctx context.Context, c *call, projectID string, in ciArgs) (any, error) {
	if in.SiteID != "" {
		path, err := sitePath(c, projectID, in.SiteID)
		if err != nil {
			return nil, err
		}
		if err := c.get(ctx, path+"/", nil, nil); err != nil {
			return nil, siteFailure(err)
		}
	}
	target := shiptemplates.SiteTarget{
		APIBase: c.settings.StudioURL, ProjectID: projectID, SiteID: in.SiteID,
		Build: shiptemplates.SiteBuild{Context: in.Context, Output: in.Output, Branch: in.Branch},
	}
	return shiptemplates.SiteCI(in.Provider, target)
}
