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
	return `name: Deploy to Excalibase
on:
  push:
    branches: [main]
permissions:
  contents: read` + packages + `
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
          push: true
          tags: ` + repository + `:${{ github.sha }}
      - name: Deploy to Excalibase
        env:
          EXCALIBASE_TOKEN: ${{ secrets.EXCALIBASE_TOKEN }}
          IMAGE: ` + repository + `@${{ steps.build.outputs.digest }}
          COMMIT_SHA: ${{ github.sha }}
        run: |
` + indent(deployScript(target), 10) + "\n"
}

// gitlabCI builds and pushes on the default branch with docker-in-docker, then deploys the digest.
func gitlabCI(target DeployTarget) string {
	login := strings.ReplaceAll(shellLogin(imageRegistry(target.Image), true), "'", "''")
	return `deploy:
  stage: deploy
  image: docker:27
  services:
    - docker:27-dind
  variables:
    DOCKER_TLS_CERTDIR: "/certs"
    IMAGE_REPOSITORY: "` + imageRepository(target.Image) + `"
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  script:
    - '` + login + `'
    - export COMMIT_SHA="$CI_COMMIT_SHA"
    - docker build -t "$IMAGE_REPOSITORY:$COMMIT_SHA" .
    - docker push "$IMAGE_REPOSITORY:$COMMIT_SHA"
    - export IMAGE="$(` + strings.ReplaceAll(pushedDigest, "'", `"`) + `)"
    - apk add --no-cache curl
    - |
` + indent(deployScript(target), 6) + "\n"
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
	deploy := strings.ReplaceAll("export COMMIT_SHA=\"$GIT_COMMIT\"\nexport IMAGE=\"$("+pushedDigest+")\"\n"+deployScript(target), `\`, `\\`)
	return `pipeline {
  agent any
  environment {
    IMAGE_REPOSITORY = '` + imageRepository(target.Image) + `'
    EXCALIBASE_TOKEN = credentials('excalibase-token')
  }
  stages {
    stage('Build and push') {
      steps {
        withCredentials([usernamePassword(credentialsId: 'registry', usernameVariable: '` + userVariable + `', passwordVariable: '` + passwordVariable + `')]) {
          sh '''
            ` + shellLogin(registry, false) + `
            docker build -t "$IMAGE_REPOSITORY:$GIT_COMMIT" .
            docker push "$IMAGE_REPOSITORY:$GIT_COMMIT"
          '''
        }
      }
    }
    stage('Deploy to Excalibase') {
      steps {
        sh '''
` + indent(deploy, 10) + `
        '''
      }
    }
  }
}
`
}
