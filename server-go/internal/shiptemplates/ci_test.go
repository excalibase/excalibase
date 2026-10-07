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
			checkGolden(t, filepath.Join("testdata", provider+"."+label+".golden"), snippet.Content)
		}
	}
}

// optionsTarget builds from a folder of a monorepo on a release branch.
func optionsTarget() DeployTarget {
	target := goldenTarget("acme/web")
	target.Build = BuildOptions{Context: "apps/poll", Dockerfile: "apps/poll/Dockerfile.prod", Branch: "release/v2"}
	return target
}

// TestCIWithBuildOptionsMatchesTheGoldenPipelines pins a pipeline for one app
// of a monorepo, the shape the examples repository had to write by hand.
func TestCIWithBuildOptionsMatchesTheGoldenPipelines(t *testing.T) {
	for _, provider := range Providers() {
		snippet, err := CI(provider, optionsTarget())
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		checkGolden(t, filepath.Join("testdata", provider+".options.golden"), snippet.Content)
	}
}

func TestGitHubPipelineTagsBranchAndShortShaAndRunsOneDeployAtATime(t *testing.T) {
	snippet, err := CI("github-actions", optionsTarget())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`branches: ["release/v2"]`, "workflow_dispatch:", "concurrency:", "cancel-in-progress: false",
		"if: github.ref_name == 'release/v2'", `context: "apps/poll"`, `file: "apps/poll/Dockerfile.prod"`,
		"acme/web:release-v2", "acme/web:${{ steps.tag.outputs.short }}", `short=${GITHUB_SHA::7}`,
	} {
		if !strings.Contains(snippet.Content, want) {
			t.Errorf("missing %q:\n%s", want, snippet.Content)
		}
	}
}

func TestCIRefusesBuildOptionsThatLeaveTheRepository(t *testing.T) {
	for name, build := range map[string]BuildOptions{
		"a climbing context":     {Context: "../other"},
		"an absolute dockerfile": {Dockerfile: "/etc/passwd"},
		"a nested climb":         {Dockerfile: "apps/../../x"},
		"a quote":                {Context: "apps/a'b"},
		"a space":                {Dockerfile: "apps/a b/Dockerfile"},
		"a branch with a space":  {Branch: "main branch"},
		"a branch expression":    {Branch: "${{ github.event }}"},
		"a branch with a climb":  {Branch: "a..b"},
	} {
		target := goldenTarget("acme/web")
		target.Build = build
		for _, provider := range Providers() {
			if _, err := CI(provider, target); err == nil {
				t.Errorf("%s: %s was not refused", provider, name)
			}
		}
	}
}

func checkGolden(t *testing.T, path, content string) {
	t.Helper()
	if *updateGolden {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if content != string(want) {
		t.Errorf("differs from %s:\n%s", path, content)
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
		for _, target := range []DeployTarget{goldenTarget("registry.example.com/team/web"), optionsTarget()} {
			yamlParses(t, provider, target)
		}
	}
}

func yamlParses(t *testing.T, provider string, target DeployTarget) {
	t.Helper()
	snippet, err := CI(provider, target)
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
