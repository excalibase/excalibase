package mcpserver

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/shiptemplates"
)

type dockerfileArgs struct {
	Stack   string `json:"stack" jsonschema:"node, nextjs, vite, python, go or java"`
	Variant string `json:"variant,omitempty" jsonschema:"node, nextjs, vite: npm or pnpm; python: gunicorn or uvicorn; java: spring-boot, spring-boot-gradle or quarkus"`
}

type ciArgs struct {
	projectArg
	Provider string `json:"provider" jsonschema:"github-actions, gitlab-ci or jenkins"`
	AppID    string `json:"app_id,omitempty" jsonschema:"the app the pipeline deploys (list_apps)"`
	Image    string `json:"image,omitempty" jsonschema:"the registry repository to push to; the provider's own registry when left out"`
}

func shippingTools() []entry {
	return []entry{
		tool("get_dockerfile_template", "A Dockerfile and .dockerignore for a stack, ready to write into the repository. Every image serves on port 8080.", readTool, getDockerfileTemplate),
		tool("get_ci_snippet", "A CI pipeline (GitHub Actions, GitLab CI or Jenkins) that builds the image, pushes it to your registry and deploys it to an app by digest.", readTool, getCISnippet),
	}
}

func getDockerfileTemplate(_ context.Context, _ *call, in dockerfileArgs) (any, error) {
	return shiptemplates.Dockerfile(in.Stack, in.Variant)
}

func getCISnippet(_ context.Context, c *call, in ciArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	appID := in.AppID
	if appID == "" {
		appID = "<app id>"
	}
	target := shiptemplates.DeployTarget{APIBase: c.settings.StudioURL, ProjectID: projectID, AppID: appID}
	return shiptemplates.CI(in.Provider, target, in.Image)
}
