package shiptemplates

import (
	"fmt"
	"path"
	"strings"
)

// SiteTarget is the site a pipeline deploys built files to (EXC-353).
type SiteTarget struct {
	// APIBase is the control plane's public origin, e.g. https://app.excalibase.io.
	APIBase   string
	ProjectID string
	// SiteID may be empty; the pipeline then carries a placeholder.
	SiteID string
	Build  SiteBuild
}

// SiteBuild says where the frontend is built and where its files land.
type SiteBuild struct {
	// Context is the folder holding package.json; the repository root when empty.
	Context string
	// Output is the build output folder inside Context; "dist" when empty.
	Output string
	// Branch is the branch whose pushes deploy, as for an app.
	Branch string
}

const (
	sitePlaceholder = "<site id>"
	siteArchive     = "site.tgz"
	siteTokenSecret = "EXCALIBASE_TOKEN: a personal access token bound to this project with the write scope, of a Developer or above"
)

// SiteCI renders the pipeline that builds the frontend, packs its output
// folder and uploads it to the site, which makes it live.
func SiteCI(provider string, target SiteTarget) (Snippet, error) {
	if err := validateSite(&target); err != nil {
		return Snippet{}, err
	}
	notes := []string{
		"The site must exist in Excalibase first (create_site, or Sites in Studio).",
		"Sites serve build output that is only files (Vite, Astro static, Next export, SvelteKit static, plain HTML); a server-rendered app (Next.js SSR, Nuxt, Remix) deploys as a container instead.",
		"The build runs npm ci and npm run build; change that step for pnpm or yarn, and set the output folder to where the build writes.",
		"Each run makes its upload live; earlier deploys stay for rollback.",
	}
	switch provider {
	case "github-actions":
		return Snippet{Path: ".github/workflows/deploy-site.yml", Content: siteGitHubActions(target), Secrets: []string{siteTokenSecret}, Notes: notes}, nil
	case "gitlab-ci":
		return Snippet{Path: ".gitlab-ci.yml", Content: siteGitLabCI(target), Secrets: []string{siteTokenSecret + "; a masked CI/CD variable"}, Notes: notes}, nil
	case "jenkins":
		return Snippet{
			Path: "Jenkinsfile", Content: siteJenkins(target),
			Secrets: []string{"excalibase-token: a Secret text credential holding " + strings.TrimPrefix(siteTokenSecret, "EXCALIBASE_TOKEN: ")},
			Notes:   append(notes, "The agent needs Node.js 24 with npm, tar and curl 7.76 or later."),
		}, nil
	case "curl":
		return Snippet{Path: "deploy-site.sh", Content: siteCurlScript(target), Secrets: []string{siteTokenSecret}, Notes: notes}, nil
	}
	return Snippet{}, fmt.Errorf("unknown CI provider %q: use %s", provider, strings.Join(Providers(), ", "))
}

func validateSite(target *SiteTarget) error {
	if !validID.MatchString(target.ProjectID) {
		return fmt.Errorf("project id must be a project id")
	}
	if target.SiteID == "" {
		target.SiteID = sitePlaceholder
	} else if !validID.MatchString(target.SiteID) {
		return fmt.Errorf("site id must be a site id")
	}
	build := target.Build
	if err := validateBuild(BuildOptions{Context: build.Context, Branch: build.Branch}); err != nil {
		return err
	}
	if build.Output != "" && (!validRepoPath.MatchString(build.Output) || climbs(build.Output)) {
		return fmt.Errorf("output must be the build output folder inside the build context, e.g. dist")
	}
	if build.Output != "" && path.Clean(build.Output) == "." {
		return fmt.Errorf("output must be the build output folder, not the build context itself: the whole folder would be published")
	}
	return nil
}

// siteOutput is the output folder from the repository root.
func siteOutput(build SiteBuild) string {
	output := build.Output
	if output == "" {
		output = "dist"
	}
	return path.Join(build.Context, output)
}

func siteDeployURL(target SiteTarget) string {
	return strings.TrimRight(target.APIBase, "/") + "/api/projects/" + target.ProjectID + "/sites/" + target.SiteID + "/deploy"
}

// siteUpload packs the output folder and uploads it; the API answers once the
// deploy is live, an error status (and so a failed step) when it is not.
func siteUpload(target SiteTarget, commitVar string) string {
	return `tar czf ` + siteArchive + ` -C "` + siteOutput(target.Build) + `" .
curl --fail-with-body -sS -X POST "` + siteDeployURL(target) + `?label=` + commitVar + `" \
  -H "Authorization: Bearer $EXCALIBASE_TOKEN" \
  -H "Content-Type: application/gzip" \
  --data-binary @` + siteArchive
}

// siteShellBuild builds in the context and returns to the repository root at rootVar.
func siteShellBuild(build SiteBuild, rootVar string) []string {
	if build.Context == "" {
		return []string{"npm ci", "npm run build"}
	}
	return []string{`cd "` + build.Context + `"`, "npm ci", "npm run build", `cd "` + rootVar + `"`}
}

func siteGitHubActions(target SiteTarget) string {
	branch := target.Build.Branch
	if branch == "" {
		branch = "main"
	}
	workingDirectory := ""
	if target.Build.Context != "" {
		workingDirectory = "\n        working-directory: \"" + target.Build.Context + "\""
	}
	return `name: Deploy site to Excalibase
on:
  push:
    branches: ["` + branch + `"]
permissions:
  contents: read
concurrency: excalibase-site-deploy
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-node@v7
        with:
          node-version: 24
      - name: Build` + workingDirectory + `
        run: |
          npm ci
          npm run build
      - name: Deploy
        env:
          EXCALIBASE_TOKEN: ${{ secrets.EXCALIBASE_TOKEN }}
        run: |
` + indent(siteUpload(target, "$GITHUB_SHA"), 10) + "\n"
}

// siteGitLabCI builds with the Node image on the default branch, then uploads.
func siteGitLabCI(target SiteTarget) string {
	branch := "$CI_DEFAULT_BRANCH"
	if target.Build.Branch != "" {
		branch = `"` + target.Build.Branch + `"`
	}
	upload := strings.SplitN(siteUpload(target, "$CI_COMMIT_SHA"), "\n", 2)
	lines := append(siteShellBuild(target.Build, "$CI_PROJECT_DIR"), upload[0])
	script := ""
	for _, line := range lines {
		script += "    - " + line + "\n"
	}
	return `deploy-site:
  stage: deploy
  image: node:24
  resource_group: excalibase-site-deploy
  rules:
    - if: $CI_COMMIT_BRANCH == ` + branch + `
  script:
` + script + `    - |
` + indent(upload[1], 6) + "\n"
}

// siteJenkins needs a Node-capable agent and a secret-text credential
// "excalibase-token". Backslashes are doubled for Groovy's triple-quoted strings.
func siteJenkins(target SiteTarget) string {
	script := strings.Join(append(siteShellBuild(target.Build, "$WORKSPACE"), siteUpload(target, "$GIT_COMMIT")), "\n")
	when := ""
	if target.Build.Branch != "" {
		when = "\n      when { anyOf { branch '" + target.Build.Branch + "'; expression { env.GIT_BRANCH == 'origin/" + target.Build.Branch + "' } } }"
	}
	return `pipeline {
  agent any
  options {
    disableConcurrentBuilds()
  }
  environment {
    EXCALIBASE_TOKEN = credentials('excalibase-token')
  }
  stages {
    stage('Build and deploy the site') {` + when + `
      steps {
        sh '''
` + indent(strings.ReplaceAll(script, `\`, `\\`), 10) + `
        '''
      }
    }
  }
}
`
}

func siteCurlScript(target SiteTarget) string {
	return "# Run from the repository root after building the site, with these set:\n" +
		"#   EXCALIBASE_TOKEN  a write token bound to this project, kept as a CI secret\n" +
		"#   COMMIT_SHA        the commit the build came from, shown in the deploy history\n" +
		"# It answers once the deploy is live, and fails the step when it is not.\n" +
		siteUpload(target, "$COMMIT_SHA") + "\n"
}
