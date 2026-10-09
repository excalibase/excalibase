package shiptemplates

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The pipelines here are the ones Studio's pipeline page shows
// (frontend/src/components/containers/ciSnippets.ts); keep the two in step.

// DeployTarget is the app a pipeline deploys to.
type DeployTarget struct {
	// APIBase is the control plane's public origin, e.g. https://app.excalibase.io.
	APIBase   string
	ProjectID string
	// AppID may be empty; the pipeline then carries a placeholder.
	AppID string
	// Image is the app's image; its repository is where CI pushes.
	Image string
	Build BuildOptions
}

// BuildOptions say where in the repository the image is built from.
type BuildOptions struct {
	// Context is the build folder; "." when empty.
	Context string
	// Dockerfile is the Dockerfile's path from the repository root; the
	// context's own Dockerfile when empty.
	Dockerfile string
	// Branch is the branch whose pushes deploy: "main" on GitHub, the
	// default branch on GitLab, when empty.
	Branch string
}

// Snippet is a rendered CI pipeline and the secrets it reads.
type Snippet struct {
	Path    string   `json:"path"`
	Content string   `json:"content"`
	Secrets []string `json:"secrets"`
	Notes   []string `json:"notes"`
}

var (
	validID = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)
	// validImage is an image reference: no quote, space or shell character can
	// reach the pipeline it is written into.
	validImage = regexp.MustCompile(`^[a-z0-9][a-z0-9._\-/:@]{0,254}$`)
	// validRepoPath and validBranch keep options to plain names inside the
	// repository: no quote, space, expression or climb reaches a pipeline.
	validRepoPath = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/\-]{0,199}$`)
	validBranch   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/\-]{0,99}$`)
	notTagChar    = regexp.MustCompile(`[^A-Za-z0-9_.\-]`)
)

var dockerHubAliases = map[string]bool{"docker.io": true, "index.docker.io": true, "registry-1.docker.io": true}

const (
	appPlaceholder = "<app id>"
	tokenSecret    = "EXCALIBASE_TOKEN: a personal access token bound to this project with the write scope"
)

// Providers lists what CI can render.
func Providers() []string { return []string{"github-actions", "gitlab-ci", "jenkins", "curl"} }

// CI renders the pipeline that builds the image, pushes it to the app's
// registry repository and deploys the pushed digest.
func CI(provider string, target DeployTarget) (Snippet, error) {
	if err := validate(&target); err != nil {
		return Snippet{}, err
	}
	notes := []string{
		"The app must exist in Excalibase first; set its port to the one the image serves on.",
		"A private image needs the registry's credentials saved in the project (Containers, Registry credentials).",
		"Deploys run one at a time; re-run the pipeline to deploy a commit again.",
	}
	switch provider {
	case "github-actions":
		return Snippet{Path: ".github/workflows/deploy.yml", Content: githubActions(target), Secrets: githubSecrets(imageRegistry(target.Image)), Notes: notes}, nil
	case "gitlab-ci":
		return Snippet{Path: ".gitlab-ci.yml", Content: gitlabCI(target), Secrets: shellSecrets(imageRegistry(target.Image), true), Notes: notes}, nil
	case "jenkins":
		return Snippet{
			Path: "Jenkinsfile", Content: jenkins(target),
			Secrets: []string{
				"excalibase-token: a Secret text credential holding " + strings.TrimPrefix(tokenSecret, "EXCALIBASE_TOKEN: "),
				"registry: a Username with password credential for the registry",
			},
			Notes: append(notes, "The agent needs docker and curl 7.76 or later."),
		}, nil
	case "curl":
		if target.Build != (BuildOptions{}) {
			notes = append(notes, "This step runs after your own build and push: set the build context, Dockerfile and branch in that build.")
		}
		return Snippet{Path: "deploy.sh", Content: curlScript(target), Secrets: []string{tokenSecret}, Notes: notes}, nil
	}
	return Snippet{}, fmt.Errorf("unknown CI provider %q: use %s", provider, strings.Join(Providers(), ", "))
}

func validate(target *DeployTarget) error {
	if !validID.MatchString(target.ProjectID) {
		return fmt.Errorf("project id must be a project id")
	}
	if target.AppID == "" {
		target.AppID = appPlaceholder
	} else if !validID.MatchString(target.AppID) {
		return fmt.Errorf("app id must be an app id")
	}
	if !validImage.MatchString(target.Image) {
		return fmt.Errorf("image must be a lowercase image reference such as ghcr.io/team/app")
	}
	return validateBuild(target.Build)
}

func validateBuild(build BuildOptions) error {
	for _, option := range [][2]string{{"build context", build.Context}, {"dockerfile", build.Dockerfile}} {
		if path := option[1]; path != "" && (!validRepoPath.MatchString(path) || climbs(path)) {
			return fmt.Errorf("%s must be a path inside the repository, e.g. apps/web", option[0])
		}
	}
	if build.Branch != "" && (!validBranch.MatchString(build.Branch) || strings.Contains(build.Branch, "..")) {
		return fmt.Errorf("branch must be a branch name, e.g. main")
	}
	return nil
}

func climbs(path string) bool {
	return slices.Contains(strings.Split(path, "/"), "..")
}

func buildContext(build BuildOptions) string {
	if build.Context == "" {
		return "."
	}
	return build.Context
}

// dockerBuildArgs is what docker build reads after its tag: the Dockerfile, when named, and the context.
func dockerBuildArgs(build BuildOptions) string {
	if build.Dockerfile == "" {
		return buildContext(build)
	}
	return "-f " + build.Dockerfile + " " + buildContext(build)
}

// branchTag is the branch as an image tag.
func branchTag(branch string) string {
	return notTagChar.ReplaceAllString(branch, "-")
}

// imageRepository is the repository part of an image reference, without its tag or digest.
func imageRepository(image string) string {
	name, _, _ := strings.Cut(image, "@")
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		return name[:colon]
	}
	return name
}

// imageRegistry is the registry host CI logs in to, or "" for Docker Hub.
func imageRegistry(image string) string {
	first, _, found := strings.Cut(imageRepository(image), "/")
	isHost := found && (strings.ContainsAny(first, ".:") || first == "localhost")
	if !isHost || dockerHubAliases[first] {
		return ""
	}
	return first
}

func appAPI(target DeployTarget) string {
	return strings.TrimRight(target.APIBase, "/") + "/api/projects/" + target.ProjectID + "/apps/" + target.AppID
}

// DeployAction is the GitHub Action the GitHub pipeline deploys with.
const DeployAction = "excalibase/deploy-action@v1"

// defaultAPIURL is the deploy action's own api-url default.
const defaultAPIURL = "https://app.excalibase.io/api"

// deployCommand deploys $IMAGE built from the commit in commitVar. With
// wait=true the API answers once the deploy has finished: 200 when it is live,
// an error status (and so a failed step) when it is not.
func deployCommand(target DeployTarget, commitVar string) string {
	return `curl --fail-with-body -sS -X POST "` + appAPI(target) + `/deploy?wait=true" \
  -H "Authorization: Bearer $EXCALIBASE_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"image\":\"$IMAGE\",\"commitSha\":\"` + commitVar + `\"}"`
}

func indent(text string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}

// pushedDigest picks the digest of this repository: a reused agent may hold
// the same image under other repositories.
func pushedDigest(commitVar string) string {
	return `docker inspect --format='{{range .RepoDigests}}{{println .}}{{end}}' "$IMAGE_REPOSITORY:` + commitVar + `" | grep "^$IMAGE_REPOSITORY@" | head -n 1`
}

func curlScript(target DeployTarget) string {
	return "# Run after pushing the image, with these set:\n" +
		"#   EXCALIBASE_TOKEN  a write token bound to this project, kept as a CI secret\n" +
		"#   IMAGE             the pushed image, best by digest: " + imageRepository(target.Image) + "@sha256:...\n" +
		"#   COMMIT_SHA        the commit the image was built from\n" +
		"# It answers once the deploy is live, and fails the step when it is not.\n" +
		deployCommand(target, "$COMMIT_SHA") + "\n"
}
