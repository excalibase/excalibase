package shiptemplates

import (
	"fmt"
	"regexp"
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
			Notes: append(notes, "The agent needs docker and curl."),
		}, nil
	case "curl":
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
	return nil
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

// deployScript is the POSIX sh step every CI runs: deploy $IMAGE with
// $COMMIT_SHA, then poll until the deploy is live or failed. Needs curl and sed.
func deployScript(target DeployTarget) string {
	return strings.Replace(deployScriptTemplate, "{{appAPI}}", appAPI(target), 1)
}

const deployScriptTemplate = `set -eu
APP_API="{{appAPI}}"
answer=$(curl -sS -X POST "$APP_API/deploy" \
  -H "Authorization: Bearer $EXCALIBASE_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"image\":\"$IMAGE\",\"commitSha\":\"$COMMIT_SHA\"}" \
  -w '\n%{http_code}')
code=$(printf '%s\n' "$answer" | tail -n 1)
body=$(printf '%s\n' "$answer" | sed '$d')
if [ "$code" != "202" ]; then
  echo "Deploy refused ($code): $body" >&2
  exit 1
fi
deploy_id=$(printf '%s' "$body" | sed -n 's/^{"id":"\([^"]*\)".*/\1/p')
if [ -z "$deploy_id" ]; then
  echo "No deploy id in: $body" >&2
  exit 1
fi
echo "Deploy $deploy_id started; waiting until it is live"
tries=0
unanswered=0
while [ "$tries" -lt 120 ]; do
  tries=$((tries + 1))
  answer=$(curl -sS "$APP_API/deploys/$deploy_id" -H "Authorization: Bearer $EXCALIBASE_TOKEN" \
    -w '\n%{http_code}' || true)
  code=$(printf '%s\n' "$answer" | tail -n 1)
  state=$(printf '%s\n' "$answer" | sed '$d')
  case "$code" in
    200) unanswered=0 ;;
    000|5??)
      unanswered=$((unanswered + 1))
      if [ "$unanswered" -ge 6 ]; then
        echo "The API did not answer ($code): $state" >&2
        exit 1
      fi
      sleep 5
      continue ;;
    *)
      echo "Polling refused ($code): $state" >&2
      exit 1 ;;
  esac
  status=$(printf '%s' "$state" | sed -n 's/.*"status":"\([a-z]*\)".*/\1/p')
  case "$status" in
    succeeded)
      echo "Live at $(printf '%s' "$state" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')"
      exit 0 ;;
    failed|superseded)
      echo "Deploy $status: $state" >&2
      exit 1 ;;
    pending|rolling) ;;
    *)
      echo "Unexpected answer: $state" >&2
      exit 1 ;;
  esac
  sleep 5
done
echo "Deploy $deploy_id did not finish within 10 minutes" >&2
exit 1`

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
const pushedDigest = `docker inspect --format='{{range .RepoDigests}}{{println .}}{{end}}' "$IMAGE_REPOSITORY:$COMMIT_SHA" | grep "^$IMAGE_REPOSITORY@" | head -n 1`

func curlScript(target DeployTarget) string {
	return "# Run after pushing the image, with these set:\n" +
		"#   EXCALIBASE_TOKEN  a write token bound to this project, kept as a CI secret\n" +
		"#   IMAGE             the pushed image, best by digest: " + imageRepository(target.Image) + "@sha256:...\n" +
		"#   COMMIT_SHA        the commit the image was built from\n" +
		deployScript(target) + "\n"
}
