package shiptemplates

import (
	"fmt"
	"regexp"
	"strings"
)

// DeployTarget is the app a pipeline deploys to.
type DeployTarget struct {
	// APIBase is the control plane's public origin, e.g. https://app.excalibase.io.
	APIBase   string
	ProjectID string
	// AppID may be empty; the pipeline then carries a placeholder.
	AppID string
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
	// validRepository is a lowercase registry repository: host[:port]/path.
	validRepository = regexp.MustCompile(`^[a-z0-9]([a-z0-9._\-]*[a-z0-9])?(:[0-9]+)?(/[a-z0-9]([a-z0-9._\-]*[a-z0-9])?)+$|^[a-z0-9]([a-z0-9._\-]*[a-z0-9])?$`)
)

const appPlaceholder = "<app id>"

// Providers lists what CI can render.
func Providers() []string { return []string{"github-actions", "gitlab-ci", "jenkins"} }

// tokenSecret is how every pipeline names the project token.
const tokenSecret = "EXCALIBASE_TOKEN: a personal access token bound to this project with the write scope (Studio, Account, Access tokens)"

const registrySecrets = "REGISTRY_USERNAME and REGISTRY_PASSWORD: the login of the registry the image is pushed to"

// CI renders the pipeline that builds the image, pushes it to the user's
// registry and deploys it by digest. image names the registry repository;
// empty uses the provider's own registry.
func CI(provider string, target DeployTarget, image string) (Snippet, error) {
	appURL, err := deployURL(target)
	if err != nil {
		return Snippet{}, err
	}
	if image != "" && !validRepository.MatchString(image) {
		return Snippet{}, fmt.Errorf("image must be a lowercase registry repository without a tag, e.g. ghcr.io/team/app")
	}
	switch provider {
	case "github-actions":
		return githubSnippet(appURL, image), nil
	case "gitlab-ci":
		return gitlabSnippet(appURL, image), nil
	case "jenkins":
		return jenkinsSnippet(appURL, image), nil
	}
	return Snippet{}, fmt.Errorf("unknown CI provider %q: use %s", provider, strings.Join(Providers(), ", "))
}

func deployURL(target DeployTarget) (string, error) {
	if !validID.MatchString(target.ProjectID) {
		return "", fmt.Errorf("project id must be a project id")
	}
	appID := target.AppID
	if appID == "" {
		appID = appPlaceholder
	} else if !validID.MatchString(appID) {
		return "", fmt.Errorf("app id must be an app id")
	}
	return strings.TrimRight(target.APIBase, "/") + "/api/projects/" + target.ProjectID + "/apps/" + appID, nil
}

// registryHost is the registry an image repository lives in; "" is Docker Hub.
func registryHost(repository string) string {
	first, _, found := strings.Cut(repository, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first
	}
	return ""
}

func commonNotes() []string {
	return []string{
		"The app must exist in Excalibase first; set its port to the one the image serves on.",
		"A private image needs the registry's credentials saved in the project (Containers, Registry credentials).",
	}
}

func githubSnippet(appURL, image string) Snippet {
	notes, secrets := commonNotes(), []string{tokenSecret}
	login := "          registry: ghcr.io\n          username: ${{ github.actor }}\n          password: ${{ secrets.GITHUB_TOKEN }}\n"
	repo := image
	if repo == "" {
		repo = "ghcr.io/${{ github.repository }}"
		notes = append(notes, "ghcr.io needs a lowercase repository name; set the image explicitly if the owner has capitals.")
	} else if host := registryHost(image); host != "ghcr.io" {
		login = "          username: ${{ secrets.REGISTRY_USERNAME }}\n          password: ${{ secrets.REGISTRY_PASSWORD }}\n"
		if host != "" {
			login = "          registry: " + host + "\n" + login
		}
		secrets = append(secrets, registrySecrets)
	}
	content := strings.Replace(fill(githubWorkflow, appURL, repo, 10), "{{login}}", login, 1)
	return Snippet{Path: ".github/workflows/deploy.yml", Content: content, Secrets: secrets, Notes: notes}
}

func gitlabSnippet(appURL, image string) Snippet {
	secrets := []string{tokenSecret + "; add it as a masked CI/CD variable"}
	repo, login := "$CI_REGISTRY_IMAGE", `echo "$CI_REGISTRY_PASSWORD" | docker login -u "$CI_REGISTRY_USER" --password-stdin "$CI_REGISTRY"`
	if image != "" {
		repo = image
		login = `echo "$REGISTRY_PASSWORD" | docker login -u "$REGISTRY_USERNAME" --password-stdin ` + registryHost(image)
		secrets = append(secrets, registrySecrets+"; masked CI/CD variables")
	}
	content := strings.Replace(fill(gitlabPipeline, appURL, repo, 6), "{{login}}", strings.TrimSpace(login), 1)
	return Snippet{Path: ".gitlab-ci.yml", Content: content, Secrets: secrets, Notes: commonNotes()}
}

func jenkinsSnippet(appURL, image string) Snippet {
	notes := append(commonNotes(), "The agent needs docker, curl and jq.")
	repo := image
	if repo == "" {
		repo = "registry.example.com/team/app"
		notes = append(notes, "Replace REGISTRY_IMAGE with your registry repository.")
	}
	content := strings.Replace(fill(jenkinsPipeline, appURL, repo, 12), "{{registry}}", registryHost(repo), 1)
	return Snippet{
		Path: "Jenkinsfile", Content: content, Notes: notes,
		Secrets: []string{
			"excalibase-token: a Secret text credential holding " + strings.TrimPrefix(tokenSecret, "EXCALIBASE_TOKEN: "),
			"registry: a Username with password credential for your registry",
		},
	}
}

// fill places the app URL, the image repository and the deploy steps,
// indented for the pipeline file they sit in.
func fill(template, appURL, repo string, indent int) string {
	pad := strings.Repeat(" ", indent)
	steps := pad + strings.ReplaceAll(strings.TrimRight(deploySteps, "\n"), "\n", "\n"+pad)
	return strings.NewReplacer("{{appURL}}", appURL, "{{repo}}", repo, "{{deploy}}", steps).Replace(template)
}

// deploySteps reads the app's version, switches it to $IMAGE at that version
// and rolls it out. IMAGE is a digest reference.
const deploySteps = `VERSION=$(curl -fsS -H "Authorization: Bearer $EXCALIBASE_TOKEN" "$EXCALIBASE_APP_URL/" | jq -r .version)
curl -fsS -X PATCH "$EXCALIBASE_APP_URL/" -H "Authorization: Bearer $EXCALIBASE_TOKEN" -H "If-Match: $VERSION" -H "Content-Type: application/json" -d "$(jq -n --arg image "$IMAGE" '{image: $image}')"
curl -fsS -X POST "$EXCALIBASE_APP_URL/deploy" -H "Authorization: Bearer $EXCALIBASE_TOKEN"
`

const githubWorkflow = `name: deploy
on:
  push:
    branches: [main]
permissions:
  contents: read
  packages: write
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
{{login}}      - id: build
        uses: docker/build-push-action@v6
        with:
          push: true
          tags: {{repo}}:${{ github.sha }}
      - name: Deploy to Excalibase
        env:
          EXCALIBASE_TOKEN: ${{ secrets.EXCALIBASE_TOKEN }}
          EXCALIBASE_APP_URL: {{appURL}}
          IMAGE: {{repo}}@${{ steps.build.outputs.digest }}
        run: |
{{deploy}}
`

const gitlabPipeline = `deploy:
  stage: deploy
  image: docker:27
  services:
    - docker:27-dind
  variables:
    DOCKER_TLS_CERTDIR: "/certs"
    EXCALIBASE_APP_URL: {{appURL}}
    TAGGED: {{repo}}:$CI_COMMIT_SHA
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  script:
    - {{login}}
    - docker build -t "$TAGGED" .
    - docker push "$TAGGED"
    - apk add --no-cache curl jq
    - |
      IMAGE=$(docker inspect --format '{{index .RepoDigests 0}}' "$TAGGED")
{{deploy}}
`

const jenkinsPipeline = `pipeline {
  agent any
  environment {
    REGISTRY = '{{registry}}'
    REGISTRY_IMAGE = '{{repo}}'
    EXCALIBASE_APP_URL = '{{appURL}}'
  }
  stages {
    stage('Build, push and deploy') {
      steps {
        withCredentials([
          usernamePassword(credentialsId: 'registry', usernameVariable: 'REGISTRY_USER', passwordVariable: 'REGISTRY_PASSWORD'),
          string(credentialsId: 'excalibase-token', variable: 'EXCALIBASE_TOKEN')
        ]) {
          sh '''
            echo "$REGISTRY_PASSWORD" | docker login -u "$REGISTRY_USER" --password-stdin $REGISTRY
            docker build -t "$REGISTRY_IMAGE:$GIT_COMMIT" .
            docker push "$REGISTRY_IMAGE:$GIT_COMMIT"
            IMAGE=$(docker inspect --format '{{index .RepoDigests 0}}' "$REGISTRY_IMAGE:$GIT_COMMIT")
{{deploy}}
          '''
        }
      }
    }
  }
}
`
