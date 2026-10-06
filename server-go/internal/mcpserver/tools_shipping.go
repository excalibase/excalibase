package mcpserver

import (
	"context"
	"errors"

	"github.com/excalibase/provisioning-poc/internal/shiptemplates"
)

type dockerfileArgs struct {
	Stack   string `json:"stack" jsonschema:"node, nextjs, vite, python, go or java"`
	Variant string `json:"variant,omitempty" jsonschema:"node, nextjs, vite: npm or pnpm; python: gunicorn or uvicorn; java: spring-boot, spring-boot-gradle or quarkus"`
}

type ciArgs struct {
	projectArg
	Provider string `json:"provider" jsonschema:"github-actions, gitlab-ci, jenkins or curl"`
	AppID    string `json:"app_id,omitempty" jsonschema:"the app the pipeline deploys (list_apps); its image names the repository CI pushes to"`
	Image    string `json:"image,omitempty" jsonschema:"the registry repository CI pushes to, e.g. ghcr.io/team/app; the app's own image when left out"`
}

func shippingTools() []entry {
	return []entry{
		tool("get_dockerfile_template", "A Dockerfile and .dockerignore for a stack, ready to write into the repository. Every image serves on port 8080.", readTool, getDockerfileTemplate),
		tool("get_ci_snippet", "A CI pipeline (GitHub Actions, GitLab CI, Jenkins, or a curl step for any other CI) that builds the image, pushes it to your registry and deploys it to an app by digest. The same pipeline Studio's pipeline page shows. Call it after the app exists (create_app, list_apps).", readTool, getCISnippet),
	}
}

func getDockerfileTemplate(_ context.Context, _ *call, in dockerfileArgs) (any, error) {
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
	target := shiptemplates.DeployTarget{APIBase: c.settings.StudioURL, ProjectID: projectID, AppID: in.AppID, Image: image}
	return shiptemplates.CI(in.Provider, target)
}
