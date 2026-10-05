package shiptemplates

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestEveryStackAndVariantRenders(t *testing.T) {
	for _, stack := range Stacks() {
		for _, variant := range stack.Variants {
			t.Run(stack.Name+"/"+variant, func(t *testing.T) {
				file, err := Dockerfile(stack.Name, variant)
				if err != nil {
					t.Fatalf("Dockerfile: %v", err)
				}
				if !strings.HasPrefix(file.Content, "# syntax=docker/dockerfile:1\n") {
					t.Errorf("must start with the syntax line:\n%s", file.Content)
				}
				if !strings.Contains(file.Content, "EXPOSE 8080") || file.Port != 8080 {
					t.Errorf("every template serves on 8080:\n%s", file.Content)
				}
				if strings.Contains(file.Content, "{{") || strings.Contains(file.Content, "<no value>") {
					t.Errorf("unfilled placeholder:\n%s", file.Content)
				}
				if file.DockerIgnore == "" {
					t.Errorf("no .dockerignore")
				}
			})
		}
	}
}

func TestDockerfileDefaultsAndRefusals(t *testing.T) {
	file, err := Dockerfile("nextjs", "")
	if err != nil || !strings.Contains(file.Content, "npm ci") || !strings.Contains(file.Content, ".next/standalone") {
		t.Fatalf("nextjs default: %v\n%s", err, file.Content)
	}
	file, err = Dockerfile("node", "pnpm")
	if err != nil || !strings.Contains(file.Content, "pnpm install --frozen-lockfile") || !strings.Contains(file.Content, "pnpm-lock.yaml") {
		t.Fatalf("node/pnpm: %v\n%s", err, file.Content)
	}
	file, err = Dockerfile("java", "quarkus")
	if err != nil || !strings.Contains(file.Content, "quarkus-run.jar") {
		t.Fatalf("java/quarkus: %v\n%s", err, file.Content)
	}
	if _, err := Dockerfile("cobol", ""); err == nil {
		t.Errorf("an unknown stack must be refused")
	}
	if _, err := Dockerfile("go", "yarn"); err == nil {
		t.Errorf("an unknown variant must be refused")
	}
}

func TestCISnippets(t *testing.T) {
	target := DeployTarget{APIBase: "https://app.example.test", ProjectID: "proj-a", AppID: "web"}
	cases := []struct {
		provider string
		path     string
		wants    []string
	}{
		{"github-actions", ".github/workflows/deploy.yml", []string{"docker/build-push-action", "steps.build.outputs.digest", "secrets.EXCALIBASE_TOKEN"}},
		{"gitlab-ci", ".gitlab-ci.yml", []string{"docker:27-dind", "$CI_REGISTRY_IMAGE", "RepoDigests"}},
		{"jenkins", "Jenkinsfile", []string{"pipeline {", "withCredentials", "excalibase-token"}},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			snippet, err := CI(tc.provider, target, "")
			if err != nil {
				t.Fatalf("CI: %v", err)
			}
			if snippet.Path != tc.path {
				t.Errorf("path = %s", snippet.Path)
			}
			wants := append([]string{
				"https://app.example.test/api/projects/proj-a/apps/web",
				`-H "If-Match: $VERSION"`, `"$EXCALIBASE_APP_URL/deploy"`,
			}, tc.wants...)
			for _, want := range wants {
				if !strings.Contains(snippet.Content, want) {
					t.Errorf("missing %q in:\n%s", want, snippet.Content)
				}
			}
			if len(snippet.Secrets) == 0 {
				t.Errorf("the snippet must say which secrets to create")
			}
		})
	}
	if _, err := CI("circleci", target, ""); err == nil {
		t.Errorf("an unknown provider must be refused")
	}
}

func TestCISnippetUsesTheNamedImage(t *testing.T) {
	snippet, err := CI("jenkins", DeployTarget{APIBase: "https://a", ProjectID: "p", AppID: "w"}, "registry.example.com/team/web")
	if err != nil || !strings.Contains(snippet.Content, "registry.example.com/team/web") {
		t.Fatalf("%v\n%s", err, snippet.Content)
	}
}

func TestCIRefusesWhatCouldBreakOutOfThePipeline(t *testing.T) {
	good := DeployTarget{APIBase: "https://a", ProjectID: "p", AppID: "w"}
	for _, image := range []string{"x' + evil + '", "registry.example.com/a b", "Ghcr.io/Upper/Case", "a\nb"} {
		if _, err := CI("jenkins", good, image); err == nil {
			t.Errorf("image %q must be refused", image)
		}
	}
	for _, appID := range []string{"w'x", "../w", "w b"} {
		if _, err := CI("github-actions", DeployTarget{APIBase: "https://a", ProjectID: "p", AppID: appID}, ""); err == nil {
			t.Errorf("app id %q must be refused", appID)
		}
	}
}

func TestCIPlaceholderAppWhenNoneIsNamed(t *testing.T) {
	snippet, err := CI("github-actions", DeployTarget{APIBase: "https://a", ProjectID: "p"}, "")
	if err != nil || !strings.Contains(snippet.Content, "/apps/<app id>") {
		t.Fatalf("%v\n%s", err, snippet.Content)
	}
}

func TestCILogsInToTheRegistryTheImageNames(t *testing.T) {
	target := DeployTarget{APIBase: "https://a", ProjectID: "p", AppID: "w"}
	gh, _ := CI("github-actions", target, "registry.example.com/team/web")
	if !strings.Contains(gh.Content, "registry: registry.example.com") || !strings.Contains(gh.Content, "secrets.REGISTRY_PASSWORD") ||
		strings.Contains(gh.Content, "secrets.GITHUB_TOKEN") {
		t.Errorf("github login:\n%s", gh.Content)
	}
	hub, _ := CI("github-actions", target, "team/web")
	if strings.Contains(hub.Content, "registry: ") {
		t.Errorf("Docker Hub needs no registry line:\n%s", hub.Content)
	}
	gl, _ := CI("gitlab-ci", target, "registry.example.com/team/web")
	if !strings.Contains(gl.Content, `"$REGISTRY_PASSWORD"`) || !strings.Contains(gl.Content, "registry.example.com") {
		t.Errorf("gitlab login:\n%s", gl.Content)
	}
	own, _ := CI("gitlab-ci", target, "")
	if !strings.Contains(own.Content, `DOCKER_TLS_CERTDIR: "/certs"`) || !strings.Contains(own.Content, "$CI_REGISTRY_PASSWORD") {
		t.Errorf("gitlab own registry:\n%s", own.Content)
	}
	jenkins, _ := CI("jenkins", target, "team/web")
	if strings.Contains(jenkins.Content, "%%/*") || !strings.Contains(jenkins.Content, "REGISTRY = ''") {
		t.Errorf("jenkins login for Docker Hub:\n%s", jenkins.Content)
	}
}

func TestYAMLPipelinesParse(t *testing.T) {
	target := DeployTarget{APIBase: "https://a", ProjectID: "p", AppID: "w"}
	for _, provider := range []string{"github-actions", "gitlab-ci"} {
		snippet, err := CI(provider, target, "")
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(snippet.Content), &parsed); err != nil {
			t.Fatalf("%s is not valid YAML: %v\n%s", provider, err, snippet.Content)
		}
		if !strings.Contains(stepText(parsed), "If-Match") {
			t.Errorf("%s: the deploy steps did not land inside a script", provider)
		}
	}
}

// stepText flattens every string in a parsed pipeline.
func stepText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var out strings.Builder
		for _, item := range typed {
			out.WriteString(stepText(item))
		}
		return out.String()
	case map[string]any:
		var out strings.Builder
		for _, item := range typed {
			out.WriteString(stepText(item))
		}
		return out.String()
	}
	return ""
}
