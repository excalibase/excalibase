package shiptemplates

import "strings"

func githubLogin(registry string) string {
	switch registry {
	case "ghcr.io":
		return "      - uses: docker/login-action@v3\n        with:\n          registry: ghcr.io\n" +
			"          username: ${{ github.actor }}\n          password: ${{ secrets.GITHUB_TOKEN }}"
	case "":
		return "      - uses: docker/login-action@v3\n        with:\n" +
			"          username: ${{ secrets.DOCKERHUB_USERNAME }}\n          password: ${{ secrets.DOCKERHUB_TOKEN }}"
	}
	return "      - uses: docker/login-action@v3\n        with:\n          registry: " + registry + "\n" +
		"          username: ${{ secrets.REGISTRY_USERNAME }}\n          password: ${{ secrets.REGISTRY_PASSWORD }}"
}

func githubSecrets(registry string) []string {
	switch registry {
	case "ghcr.io":
		return []string{tokenSecret}
	case "":
		return []string{tokenSecret, "DOCKERHUB_USERNAME and DOCKERHUB_TOKEN: the Docker Hub login"}
	}
	return []string{tokenSecret, "REGISTRY_USERNAME and REGISTRY_PASSWORD: the login of " + registry}
}

// shellLogin is the docker login a shell runs, with the secrets each CI holds for the registry.
func shellLogin(registry string, gitlab bool) string {
	switch {
	case gitlab && registry == "registry.gitlab.com":
		return `echo "$CI_REGISTRY_PASSWORD" | docker login -u "$CI_REGISTRY_USER" --password-stdin "$CI_REGISTRY"`
	case registry == "":
		return `echo "$DOCKERHUB_TOKEN" | docker login -u "$DOCKERHUB_USERNAME" --password-stdin`
	}
	return `echo "$REGISTRY_PASSWORD" | docker login -u "$REGISTRY_USERNAME" --password-stdin ` + registry
}

func shellSecrets(registry string, gitlab bool) []string {
	token := tokenSecret + "; a masked CI/CD variable"
	switch {
	case gitlab && registry == "registry.gitlab.com":
		return []string{token}
	case registry == "":
		return []string{token, "DOCKERHUB_USERNAME and DOCKERHUB_TOKEN: masked variables with the Docker Hub login"}
	}
	return []string{token, "REGISTRY_USERNAME and REGISTRY_PASSWORD: masked variables with the login of " + registry}
}

func githubActions(target DeployTarget) string {
	repository, registry := imageRepository(target.Image), imageRegistry(target.Image)
	packages := ""
	if registry == "ghcr.io" {
		packages = "\n  packages: write"
	}
	branch := target.Build.Branch
	if branch == "" {
		branch = "main"
	}
	file := ""
	if target.Build.Dockerfile != "" {
		file = "\n          file: \"" + target.Build.Dockerfile + "\""
	}
	apiURL := ""
	if api := strings.TrimRight(target.APIBase, "/") + "/api"; api != defaultAPIURL {
		apiURL = "\n          api-url: " + api
	}
	return `name: Deploy to Excalibase
on:
  push:
    branches: ["` + branch + `"]
permissions:
  contents: read` + packages + `
concurrency: excalibase-deploy
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
` + githubLogin(registry) + `
      - id: build
        uses: docker/build-push-action@v6
        with:
          context: "` + buildContext(target.Build) + `"` + file + `
          push: true
          tags: ` + repository + `:` + branchTag(branch) + `,` + repository + `:${{ github.sha }}
      - uses: ` + DeployAction + `
        with:
          app: ` + target.ProjectID + `/` + target.AppID + `
          image: ` + repository + `@${{ steps.build.outputs.digest }}
          token: ${{ secrets.EXCALIBASE_TOKEN }}` + apiURL + "\n"
}

// gitlabCI builds and pushes on the default branch with docker-in-docker, then deploys the digest.
func gitlabCI(target DeployTarget) string {
	login := strings.ReplaceAll(shellLogin(imageRegistry(target.Image), true), "'", "''")
	branch := "$CI_DEFAULT_BRANCH"
	if target.Build.Branch != "" {
		branch = `"` + target.Build.Branch + `"`
	}
	return `deploy:
  stage: deploy
  image: docker:27
  services:
    - docker:27-dind
  resource_group: excalibase-deploy
  variables:
    DOCKER_TLS_CERTDIR: "/certs"
    IMAGE_REPOSITORY: "` + imageRepository(target.Image) + `"
  rules:
    - if: $CI_COMMIT_BRANCH == ` + branch + `
  script:
    - '` + login + `'
    - docker build -t "$IMAGE_REPOSITORY:$CI_COMMIT_SHA" ` + dockerBuildArgs(target.Build) + `
    - docker push "$IMAGE_REPOSITORY:$CI_COMMIT_SHA"
    - IMAGE="$(` + strings.ReplaceAll(pushedDigest("$CI_COMMIT_SHA"), "'", `"`) + `)"
    - apk add --no-cache curl
    - |
` + indent(deployCommand(target, "$CI_COMMIT_SHA"), 6) + "\n"
}

// jenkins needs a Docker-capable agent, a username/password credential
// "registry" and a secret-text credential "excalibase-token". Groovy reads
// backslashes inside ”' strings, so each one is doubled to reach sh as written.
func jenkins(target DeployTarget) string {
	registry := imageRegistry(target.Image)
	userVariable, passwordVariable := "REGISTRY_USERNAME", "REGISTRY_PASSWORD"
	if registry == "" {
		userVariable, passwordVariable = "DOCKERHUB_USERNAME", "DOCKERHUB_TOKEN"
	}
	script := shellLogin(registry, false) + "\n" +
		`docker build -t "$IMAGE_REPOSITORY:$GIT_COMMIT" ` + dockerBuildArgs(target.Build) + "\n" +
		`docker push "$IMAGE_REPOSITORY:$GIT_COMMIT"` + "\n" +
		`IMAGE="$(` + pushedDigest("$GIT_COMMIT") + `)"` + "\n" +
		deployCommand(target, "$GIT_COMMIT")
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
    IMAGE_REPOSITORY = '` + imageRepository(target.Image) + `'
    EXCALIBASE_TOKEN = credentials('excalibase-token')
  }
  stages {
    stage('Build, push and deploy') {` + when + `
      steps {
        withCredentials([usernamePassword(credentialsId: 'registry', usernameVariable: '` + userVariable + `', passwordVariable: '` + passwordVariable + `')]) {
          sh '''
` + indent(strings.ReplaceAll(script, `\`, `\\`), 12) + `
          '''
        }
      }
    }
  }
}
`
}
