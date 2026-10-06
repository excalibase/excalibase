package shiptemplates

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

var updateGolden = flag.Bool("update", false, "rewrite the CI golden files")

// goldenImages covers each login a pipeline can need: ghcr.io, Docker Hub and
// any other registry.
var goldenImages = map[string]string{
	"ghcr":      "ghcr.io/acme/web:main",
	"dockerhub": "acme/web",
	"registry":  "registry.example.com/team/web@sha256:0123",
}

func goldenTarget(image string) DeployTarget {
	return DeployTarget{APIBase: "https://app.example.test", ProjectID: "proj-a", AppID: "web", Image: image}
}

// TestCIMatchesTheGoldenPipelines pins the pipelines to testdata, which
// Studio's pipeline page is tested against too (ciSnippets.node.test.ts), so
// get_ci_snippet and Studio hand out the same pipeline.
func TestCIMatchesTheGoldenPipelines(t *testing.T) {
	for label, image := range goldenImages {
		for _, provider := range Providers() {
			snippet, err := CI(provider, goldenTarget(image))
			if err != nil {
				t.Fatalf("%s %s: %v", provider, label, err)
			}
			path := filepath.Join("testdata", provider+"."+label+".golden")
			if *updateGolden {
				if err := os.WriteFile(path, []byte(snippet.Content), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if snippet.Content != string(want) {
				t.Errorf("%s differs from %s:\n%s", provider, path, snippet.Content)
			}
		}
	}
}

func TestCIDeploysThePushedDigestAndPolls(t *testing.T) {
	for _, provider := range Providers() {
		snippet, err := CI(provider, goldenTarget("ghcr.io/acme/web"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"https://app.example.test/api/projects/proj-a/apps/web",
			`-X POST "$APP_API/deploy"`, `"$APP_API/deploys/$deploy_id"`, `commitSha`,
		} {
			if !strings.Contains(snippet.Content, want) {
				t.Errorf("%s: missing %q", provider, want)
			}
		}
		if len(snippet.Secrets) == 0 {
			t.Errorf("%s: the snippet must say which secrets to create", provider)
		}
	}
}

func TestCISecretsFollowTheRegistry(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/acme/web":              "",
		"acme/web":                      "DOCKERHUB_TOKEN",
		"docker.io/acme/web":            "DOCKERHUB_TOKEN",
		"registry.example.com/team/web": "REGISTRY_PASSWORD",
	}
	for image, want := range cases {
		snippet, err := CI("github-actions", goldenTarget(image))
		if err != nil {
			t.Fatal(err)
		}
		secrets := strings.Join(snippet.Secrets, " ")
		if want == "" && len(snippet.Secrets) != 1 || want != "" && !strings.Contains(secrets, want) {
			t.Errorf("%s: secrets %v", image, snippet.Secrets)
		}
	}
}

func TestCIRefusesWhatCouldBreakOutOfThePipeline(t *testing.T) {
	for _, image := range []string{"", "x' + evil + '", "registry.example.com/a b", "Ghcr.io/Upper/Case", "a\nb", `a"b`, "a$b"} {
		if _, err := CI("jenkins", goldenTarget(image)); err == nil {
			t.Errorf("image %q must be refused", image)
		}
	}
	for _, appID := range []string{"w'x", "../w", "w b"} {
		target := goldenTarget("ghcr.io/acme/web")
		target.AppID = appID
		if _, err := CI("github-actions", target); err == nil {
			t.Errorf("app id %q must be refused", appID)
		}
	}
	if _, err := CI("circleci", goldenTarget("ghcr.io/acme/web")); err == nil {
		t.Errorf("an unknown provider must be refused")
	}
}

func TestCIPlaceholderAppWhenNoneIsNamed(t *testing.T) {
	target := goldenTarget("ghcr.io/acme/web")
	target.AppID = ""
	snippet, err := CI("curl", target)
	if err != nil || !strings.Contains(snippet.Content, "/apps/<app id>") {
		t.Fatalf("%v\n%s", err, snippet.Content)
	}
}

func TestYAMLPipelinesParse(t *testing.T) {
	for _, provider := range []string{"github-actions", "gitlab-ci"} {
		snippet, err := CI(provider, goldenTarget("registry.example.com/team/web"))
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(snippet.Content), &parsed); err != nil {
			t.Fatalf("%s is not valid YAML: %v\n%s", provider, err, snippet.Content)
		}
		if !strings.Contains(stepText(parsed), `"$APP_API/deploy"`) {
			t.Errorf("%s: the deploy script did not land inside a step", provider)
		}
	}
}

// stepText flattens every string in a parsed pipeline.
func stepText(value any) string {
	var out strings.Builder
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		for _, item := range typed {
			out.WriteString(stepText(item))
		}
	case map[string]any:
		for _, item := range typed {
			out.WriteString(stepText(item))
		}
	}
	return out.String()
}
