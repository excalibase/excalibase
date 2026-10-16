package shiptemplates

import (
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func siteTarget() SiteTarget {
	return SiteTarget{APIBase: "https://app.example.test", ProjectID: "proj-a", SiteID: "0b8f2c1e-5d1a-4c7e-9f2a-3b4c5d6e7f80"}
}

// siteOptionsTarget builds one frontend of a monorepo on a release branch.
func siteOptionsTarget() SiteTarget {
	target := siteTarget()
	target.Build = SiteBuild{Context: "apps/web", Output: "build", Branch: "release/v2"}
	return target
}

func TestSiteCIMatchesTheGoldenPipelines(t *testing.T) {
	for _, provider := range Providers() {
		for label, target := range map[string]SiteTarget{"": siteTarget(), ".options": siteOptionsTarget()} {
			snippet, err := SiteCI(provider, target)
			if err != nil {
				t.Fatalf("%s%s: %v", provider, label, err)
			}
			checkGolden(t, filepath.Join("testdata", "site-"+provider+label+".golden"), snippet.Content)
		}
	}
}

// Every pipeline builds, packs the output folder and uploads it to the site's deploy route with the CI secret.
func TestSiteCIBuildsPacksAndUploads(t *testing.T) {
	for _, provider := range Providers() {
		snippet, err := SiteCI(provider, siteOptionsTarget())
		if err != nil {
			t.Fatal(err)
		}
		wants := []string{
			`tar czf site.tgz -C "apps/web/build" .`,
			`curl --fail-with-body -sS -X POST "https://app.example.test/api/projects/proj-a/sites/0b8f2c1e-5d1a-4c7e-9f2a-3b4c5d6e7f80/deploy?label=`,
			`-H "Authorization: Bearer $EXCALIBASE_DEPLOY_TOKEN"`, `--data-binary @site.tgz`, `-H "Content-Type: application/gzip"`,
		}
		if provider != "curl" {
			wants = append(wants, "npm ci", "npm run build")
		}
		for _, want := range wants {
			if !strings.Contains(snippet.Content, want) {
				t.Errorf("%s: missing %q:\n%s", provider, want, snippet.Content)
			}
		}
		secrets := strings.Join(snippet.Secrets, " ")
		if !strings.Contains(secrets, "EXCALIBASE_DEPLOY_TOKEN") && !strings.Contains(secrets, "excalibase-deploy-token") {
			t.Errorf("%s: the snippet must say which secret to create: %v", provider, snippet.Secrets)
		}
		// EXC-620: the pipeline holds the site's deploy token, never a personal access token.
		if !strings.Contains(secrets, "deploy token") || strings.Contains(secrets, "personal access token") || strings.Contains(snippet.Content, "EXCALIBASE_TOKEN") {
			t.Errorf("%s: the snippet must use the site's deploy token: %v", provider, snippet.Secrets)
		}
	}
}

func TestSiteCIDefaultsToDistAtTheRepositoryRoot(t *testing.T) {
	snippet, err := SiteCI("github-actions", siteTarget())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`tar czf site.tgz -C "dist" .`, `branches: ["main"]`, "actions/checkout@v7", "actions/setup-node@v7", "concurrency: excalibase-site-deploy"} {
		if !strings.Contains(snippet.Content, want) {
			t.Errorf("missing %q:\n%s", want, snippet.Content)
		}
	}
	// The commit rides as a variable the runner sets, never as an expression in the script.
	if !strings.Contains(snippet.Content, "label=$GITHUB_SHA") {
		t.Errorf("label must come from GITHUB_SHA:\n%s", snippet.Content)
	}
}

func TestSiteCIPlaceholderWhenNoSiteIsNamed(t *testing.T) {
	target := siteTarget()
	target.SiteID = ""
	snippet, err := SiteCI("curl", target)
	if err != nil || !strings.Contains(snippet.Content, "/sites/<site id>/deploy") {
		t.Fatalf("%v\n%s", err, snippet.Content)
	}
}

func TestSiteCIRefusesWhatCouldLeaveTheRepositoryOrThePipeline(t *testing.T) {
	for name, build := range map[string]SiteBuild{
		"a climbing output":         {Output: "../secrets"},
		"an absolute output":        {Output: "/etc"},
		"the repository itself":     {Output: "."},
		"the context itself":        {Context: "apps/web", Output: "./"},
		"a climbing context":        {Context: "../other"},
		"a quote in the output":     {Output: `dist"x`},
		"a space in the context":    {Context: "apps/a b"},
		"a branch expression":       {Branch: "${{ github.event }}"},
		"a branch with a climb":     {Branch: "a..b"},
		"a shell variable in paths": {Output: "$HOME"},
	} {
		target := siteTarget()
		target.Build = build
		for _, provider := range Providers() {
			if _, err := SiteCI(provider, target); err == nil {
				t.Errorf("%s: %s was not refused", provider, name)
			}
		}
	}
	for _, siteID := range []string{"w'x", "../w", "w b"} {
		target := siteTarget()
		target.SiteID = siteID
		if _, err := SiteCI("jenkins", target); err == nil {
			t.Errorf("site id %q must be refused", siteID)
		}
	}
	bad := siteTarget()
	bad.ProjectID = "p/q"
	if _, err := SiteCI("curl", bad); err == nil {
		t.Errorf("a project id with a slash must be refused")
	}
	if _, err := SiteCI("circleci", siteTarget()); err == nil {
		t.Errorf("an unknown provider must be refused")
	}
}

func TestSiteYAMLPipelinesParse(t *testing.T) {
	for _, provider := range []string{"github-actions", "gitlab-ci"} {
		for _, target := range []SiteTarget{siteTarget(), siteOptionsTarget()} {
			snippet, err := SiteCI(provider, target)
			if err != nil {
				t.Fatal(err)
			}
			var parsed map[string]any
			if err := yaml.Unmarshal([]byte(snippet.Content), &parsed); err != nil {
				t.Fatalf("%s is not valid YAML: %v\n%s", provider, err, snippet.Content)
			}
			if !strings.Contains(stepText(parsed), "--data-binary @site.tgz") {
				t.Errorf("%s: the upload did not land inside a step", provider)
			}
		}
	}
}
