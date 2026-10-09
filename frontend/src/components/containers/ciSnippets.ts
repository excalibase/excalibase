// Copy-paste CI setups that build an image in the user's own CI, push it to
// their registry, and deploy the pushed digest through the deploy API (ADR 0037).

export interface SnippetTarget {
  // The control-plane API, absolute, such as https://app.excalibase.io/api.
  readonly apiUrl: string;
  readonly projectId: string;
  readonly appId: string;
  // The app's image as stored; its repository is where CI pushes.
  readonly image: string;
  readonly build?: BuildOptions;
}

// Where in the repository the image is built from (server: shiptemplates.BuildOptions).
export interface BuildOptions {
  // The build folder; "." when empty.
  readonly context?: string;
  // The Dockerfile's path from the repository root; the context's own when empty.
  readonly dockerfile?: string;
  // The branch whose pushes deploy: main on GitHub, the default branch on GitLab, when empty.
  readonly branch?: string;
}

// Options stay plain names inside the repository: no quote, space, expression
// or climb reaches a pipeline (server: shiptemplates.validateBuild).
const REPO_PATH = /^[A-Za-z0-9._][A-Za-z0-9._/-]{0,199}$/;
const BRANCH = /^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$/;

export function validateBuild(build?: BuildOptions): void {
  for (const [name, value] of [['build context', build?.context], ['dockerfile', build?.dockerfile]] as const) {
    if (value && (!REPO_PATH.test(value) || value.split('/').includes('..'))) {
      throw new Error(`${name} must be a path inside the repository, e.g. apps/web`);
    }
  }
  if (build?.branch && (!BRANCH.test(build.branch) || build.branch.includes('..'))) {
    throw new Error('branch must be a branch name, e.g. main');
  }
}

const buildContext = (build?: BuildOptions) => build?.context || '.';

// What docker build reads after its tag: the Dockerfile, when named, and the context.
const dockerBuildArgs = (build?: BuildOptions) =>
  build?.dockerfile ? `-f ${build.dockerfile} ${buildContext(build)}` : buildContext(build);

// The branch as an image tag.
const branchTag = (branch: string) => branch.replace(/[^A-Za-z0-9_.-]/g, '-');

const DOCKER_HUB_ALIASES = new Set(['docker.io', 'index.docker.io', 'registry-1.docker.io']);

// The repository part of an image reference, without its tag or digest.
export function imageRepository(image: string): string {
  const name = image.split('@')[0];
  const colon = name.lastIndexOf(':');
  return colon > name.lastIndexOf('/') ? name.slice(0, colon) : name;
}

// The registry host CI logs in to, or null for Docker Hub.
export function imageRegistry(image: string): string | null {
  const [first, ...rest] = imageRepository(image).split('/');
  const isHost = rest.length > 0 && (first.includes('.') || first.includes(':') || first === 'localhost');
  if (!isHost || DOCKER_HUB_ALIASES.has(first.toLowerCase())) return null;
  return first.toLowerCase();
}

const appApi = (target: SnippetTarget) =>
  `${target.apiUrl.replace(/\/+$/, '')}/projects/${target.projectId}/apps/${target.appId}`;

// The GitHub Action the GitHub pipeline deploys with, and its own api-url default.
export const DEPLOY_ACTION = 'excalibase/deploy-action@v1';
const DEFAULT_API_URL = 'https://app.excalibase.io/api';

// Deploys $IMAGE built from the commit in commitVar. With wait=true the API
// answers once the deploy has finished: 200 when it is live, an error status
// (and so a failed step) when it is not.
export function deployCommand(target: SnippetTarget, commitVar: string): string {
  return `curl --fail-with-body -sS -X POST "${appApi(target)}/deploy?wait=true" \\
  -H "Authorization: Bearer $EXCALIBASE_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d "{\\"image\\":\\"$IMAGE\\",\\"commitSha\\":\\"${commitVar}\\"}"`;
}

const indent = (text: string, spaces: number) =>
  text
    .split('\n')
    .map((line) => (line === '' ? line : ' '.repeat(spaces) + line))
    .join('\n');

function githubLogin(registry: string | null): string {
  if (registry === 'ghcr.io') {
    return `      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: \${{ github.actor }}
          password: \${{ secrets.GITHUB_TOKEN }}`;
  }
  if (registry === null) {
    return `      - uses: docker/login-action@v3
        with:
          username: \${{ secrets.DOCKERHUB_USERNAME }}
          password: \${{ secrets.DOCKERHUB_TOKEN }}`;
  }
  return `      - uses: docker/login-action@v3
        with:
          registry: ${registry}
          username: \${{ secrets.REGISTRY_USERNAME }}
          password: \${{ secrets.REGISTRY_PASSWORD }}`;
}

// The docker login a shell runs, with the secrets each CI holds for the registry.
function shellLogin(registry: string | null, gitlab: boolean): string {
  if (gitlab && registry === 'registry.gitlab.com') {
    return 'echo "$CI_REGISTRY_PASSWORD" | docker login -u "$CI_REGISTRY_USER" --password-stdin "$CI_REGISTRY"';
  }
  if (registry === null) {
    return 'echo "$DOCKERHUB_TOKEN" | docker login -u "$DOCKERHUB_USERNAME" --password-stdin';
  }
  return `echo "$REGISTRY_PASSWORD" | docker login -u "$REGISTRY_USERNAME" --password-stdin ${registry}`;
}

// A reused agent may hold the same image under other repositories; only this one's digest is deployed.
const pushedDigest = (commitVar: string) =>
  `docker inspect --format='{{range .RepoDigests}}{{println .}}{{end}}' "$IMAGE_REPOSITORY:${commitVar}" | grep "^$IMAGE_REPOSITORY@" | head -n 1`;

// .gitlab-ci.yml: build and push on the default branch with docker-in-docker, then deploy the digest.
export function gitlabCiSnippet(target: SnippetTarget): string {
  validateBuild(target.build);
  const login = shellLogin(imageRegistry(target.image), true).replaceAll("'", "''");
  const branch = target.build?.branch ? `"${target.build.branch}"` : '$CI_DEFAULT_BRANCH';
  return `deploy:
  stage: deploy
  image: docker:27
  services:
    - docker:27-dind
  resource_group: excalibase-deploy
  variables:
    DOCKER_TLS_CERTDIR: "/certs"
    IMAGE_REPOSITORY: "${imageRepository(target.image)}"
  rules:
    - if: $CI_COMMIT_BRANCH == ${branch}
  script:
    - '${login}'
    - docker build -t "$IMAGE_REPOSITORY:$CI_COMMIT_SHA" ${dockerBuildArgs(target.build)}
    - docker push "$IMAGE_REPOSITORY:$CI_COMMIT_SHA"
    - IMAGE="$(${pushedDigest('$CI_COMMIT_SHA').replaceAll("'", '"')})"
    - apk add --no-cache curl
    - |
${indent(deployCommand(target, '$CI_COMMIT_SHA'), 6)}
`;
}

// Groovy reads backslashes inside ''' strings, so each one is doubled to reach sh as written.
const groovyShell = (script: string) => script.replaceAll('\\', '\\\\');

// Jenkinsfile: needs a Docker-capable agent, a username/password credential
// "registry" and a secret-text credential "excalibase-token".
export function jenkinsSnippet(target: SnippetTarget): string {
  const registry = imageRegistry(target.image);
  const [userVariable, passwordVariable] =
    registry === null ? ['DOCKERHUB_USERNAME', 'DOCKERHUB_TOKEN'] : ['REGISTRY_USERNAME', 'REGISTRY_PASSWORD'];
  validateBuild(target.build);
  const branch = target.build?.branch;
  const when = branch
    ? `\n      when { anyOf { branch '${branch}'; expression { env.GIT_BRANCH == 'origin/${branch}' } } }`
    : '';
  const script = [
    shellLogin(registry, false),
    `docker build -t "$IMAGE_REPOSITORY:$GIT_COMMIT" ${dockerBuildArgs(target.build)}`,
    'docker push "$IMAGE_REPOSITORY:$GIT_COMMIT"',
    `IMAGE="$(${pushedDigest('$GIT_COMMIT')})"`,
    deployCommand(target, '$GIT_COMMIT'),
  ].join('\n');
  return `pipeline {
  agent any
  options {
    disableConcurrentBuilds()
  }
  environment {
    IMAGE_REPOSITORY = '${imageRepository(target.image)}'
    EXCALIBASE_TOKEN = credentials('excalibase-token')
  }
  stages {
    stage('Build, push and deploy') {${when}
      steps {
        withCredentials([usernamePassword(credentialsId: 'registry', usernameVariable: '${userVariable}', passwordVariable: '${passwordVariable}')]) {
          sh '''
${indent(groovyShell(script), 12)}
          '''
        }
      }
    }
  }
}
`;
}

// Any other CI: one shell step after the image is pushed.
export function curlSnippet(target: SnippetTarget): string {
  return `# Run after pushing the image, with these set:
#   EXCALIBASE_TOKEN  a write token bound to this project, kept as a CI secret
#   IMAGE             the pushed image, best by digest: ${imageRepository(target.image)}@sha256:...
#   COMMIT_SHA        the commit the image was built from
# It answers once the deploy is live, and fails the step when it is not.
${deployCommand(target, '$COMMIT_SHA')}
`;
}

// .github/workflows/deploy.yml: build and push on every push to the branch, then deploy the digest with the action.
export function githubActionsSnippet(target: SnippetTarget): string {
  const repository = imageRepository(target.image);
  const registry = imageRegistry(target.image);
  const packages = registry === 'ghcr.io' ? '\n  packages: write' : '';
  validateBuild(target.build);
  const branch = target.build?.branch || 'main';
  const file = target.build?.dockerfile ? `\n          file: "${target.build.dockerfile}"` : '';
  const apiUrl = target.apiUrl.replace(/\/+$/, '');
  const apiInput = apiUrl === DEFAULT_API_URL ? '' : `\n          api-url: ${apiUrl}`;
  return `name: Deploy to Excalibase
on:
  push:
    branches: ["${branch}"]
permissions:
  contents: read${packages}
concurrency: excalibase-deploy
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
${githubLogin(registry)}
      - id: build
        uses: docker/build-push-action@v6
        with:
          context: "${buildContext(target.build)}"${file}
          push: true
          tags: ${repository}:${branchTag(branch)},${repository}:\${{ github.sha }}
      - uses: ${DEPLOY_ACTION}
        with:
          app: ${target.projectId}/${target.appId}
          image: ${repository}@\${{ steps.build.outputs.digest }}
          token: \${{ secrets.EXCALIBASE_TOKEN }}${apiInput}
`;
}
