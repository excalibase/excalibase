package mcpserver

import (
	"context"
	"errors"

	"github.com/excalibase/provisioning-poc/internal/shiptemplates"
)

type dockerfileArgs struct {
	Stack   string `json:"stack,omitempty" jsonschema:"node, nextjs, vite, static, python, go or java; leave it out when unsure to get what to look for and what to ask"`
	Variant string `json:"variant,omitempty" jsonschema:"node, nextjs, vite: npm or pnpm; python: gunicorn or uvicorn; java: spring-boot, spring-boot-gradle or quarkus"`
}

type ciArgs struct {
	projectArg
	Provider   string `json:"provider" jsonschema:"github-actions, gitlab-ci, jenkins or curl"`
	AppID      string `json:"app_id,omitempty" jsonschema:"the app the pipeline deploys (list_apps); its image names the repository CI pushes to"`
	Image      string `json:"image,omitempty" jsonschema:"the registry repository CI pushes to, e.g. ghcr.io/team/app; the app's own image when left out"`
	Context    string `json:"context,omitempty" jsonschema:"the build folder inside the repository, e.g. apps/web for a monorepo; the repository root when left out"`
	Dockerfile string `json:"dockerfile,omitempty" jsonschema:"the Dockerfile's path from the repository root, e.g. apps/web/Dockerfile; the context's Dockerfile when left out"`
	Branch     string `json:"branch,omitempty" jsonschema:"the branch whose pushes deploy; main (GitHub) or the default branch (GitLab) when left out. GitHub tags the image with it and the short commit sha"`
}

func shippingTools() []entry {
	return []entry{
		tool("get_dockerfile_template", "A Dockerfile and .dockerignore for a stack, ready to write into the repository. Every image serves on port 8080 (PORT), "+
			"runs as a non-root user and names its health check path. When the stack is not clear from the repository, call it without a stack: "+
			"it answers what to look for and what to ask the user, instead of guessing.", readTool, getDockerfileTemplate),
		tool("get_ci_snippet", "A CI pipeline (GitHub Actions, GitLab CI, Jenkins, or a curl step for any other CI) that builds the image, pushes it to your registry and deploys it to an app by digest. The same pipeline Studio's pipeline page shows. Call it after the app exists (create_app, list_apps). "+
			"For a monorepo pass context and dockerfile; deploys run one at a time and the pipeline can be started by hand.", readTool, getCISnippet),
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
