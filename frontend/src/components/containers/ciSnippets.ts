// Copy-paste CI setups that build an image in the user's own CI, push it to
// their registry, and deploy the pushed digest through the deploy API (ADR 0037).

export interface SnippetTarget {
  // The control-plane API, absolute, such as https://app.excalibase.io/api.
  readonly apiUrl: string;
  readonly projectId: string;
  readonly appId: string;
  // The app's image as stored; its repository is where CI pushes.
  readonly image: string;
}

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

// One POSIX sh script every CI runs: deploy $IMAGE with $COMMIT_SHA, then poll
// until the deploy is live or failed. Needs only curl and sed.
export function deployScript(target: SnippetTarget): string {
  return `set -eu
APP_API="${appApi(target)}"
answer=$(curl -sS -X POST "$APP_API/deploy" \\
  -H "Authorization: Bearer $EXCALIBASE_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d "{\\"image\\":\\"$IMAGE\\",\\"commitSha\\":\\"$COMMIT_SHA\\"}" \\
  -w '\\n%{http_code}')
code=$(printf '%s\\n' "$answer" | tail -n 1)
body=$(printf '%s\\n' "$answer" | sed '$d')
if [ "$code" != "202" ]; then
  echo "Deploy refused ($code): $body" >&2
  exit 1
fi
deploy_id=$(printf '%s' "$body" | sed -n 's/^{"id":"\\([^"]*\\)".*/\\1/p')
if [ -z "$deploy_id" ]; then
  echo "No deploy id in: $body" >&2
  exit 1
fi
echo "Deploy $deploy_id started; waiting until it is live"
tries=0
unanswered=0
while [ "$tries" -lt 120 ]; do
  tries=$((tries + 1))
  answer=$(curl -sS "$APP_API/deploys/$deploy_id" -H "Authorization: Bearer $EXCALIBASE_TOKEN" \\
    -w '\\n%{http_code}' || true)
  code=$(printf '%s\\n' "$answer" | tail -n 1)
  state=$(printf '%s\\n' "$answer" | sed '$d')
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
  status=$(printf '%s' "$state" | sed -n 's/.*"status":"\\([a-z]*\\)".*/\\1/p')
  case "$status" in
    succeeded)
      echo "Live at $(printf '%s' "$state" | sed -n 's/.*"url":"\\([^"]*\\)".*/\\1/p')"
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
exit 1`;
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
const PUSHED_DIGEST = `docker inspect --format='{{range .RepoDigests}}{{println .}}{{end}}' "$IMAGE_REPOSITORY:$COMMIT_SHA" | grep "^$IMAGE_REPOSITORY@" | head -n 1`;

// .gitlab-ci.yml: build and push on the default branch with docker-in-docker, then deploy the digest.
export function gitlabCiSnippet(target: SnippetTarget): string {
  const login = shellLogin(imageRegistry(target.image), true).replaceAll("'", "''");
  return `deploy:
  stage: deploy
  image: docker:27
  services:
    - docker:27-dind
  variables:
    DOCKER_TLS_CERTDIR: "/certs"
    IMAGE_REPOSITORY: "${imageRepository(target.image)}"
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  script:
    - '${login}'
    - export COMMIT_SHA="$CI_COMMIT_SHA"
    - docker build -t "$IMAGE_REPOSITORY:$COMMIT_SHA" .
    - docker push "$IMAGE_REPOSITORY:$COMMIT_SHA"
    - export IMAGE="$(${PUSHED_DIGEST.replaceAll("'", '"')})"
    - apk add --no-cache curl
    - |
${indent(deployScript(target), 6)}
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
  return `pipeline {
  agent any
  environment {
    IMAGE_REPOSITORY = '${imageRepository(target.image)}'
    EXCALIBASE_TOKEN = credentials('excalibase-token')
  }
  stages {
    stage('Build and push') {
      steps {
        withCredentials([usernamePassword(credentialsId: 'registry', usernameVariable: '${userVariable}', passwordVariable: '${passwordVariable}')]) {
          sh '''
            ${shellLogin(registry, false)}
            docker build -t "$IMAGE_REPOSITORY:$GIT_COMMIT" .
            docker push "$IMAGE_REPOSITORY:$GIT_COMMIT"
          '''
        }
      }
    }
    stage('Deploy to Excalibase') {
      steps {
        sh '''
${indent(groovyShell(`export COMMIT_SHA="$GIT_COMMIT"\nexport IMAGE="$(${PUSHED_DIGEST})"\n${deployScript(target)}`), 10)}
        '''
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
${deployScript(target)}
`;
}

// .github/workflows/deploy.yml: build and push on every push to main, then deploy the digest.
export function githubActionsSnippet(target: SnippetTarget): string {
  const repository = imageRepository(target.image);
  const registry = imageRegistry(target.image);
  const packages = registry === 'ghcr.io' ? '\n  packages: write' : '';
  return `name: Deploy to Excalibase
on:
  push:
    branches: [main]
permissions:
  contents: read${packages}
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
          push: true
          tags: ${repository}:\${{ github.sha }}
      - name: Deploy to Excalibase
        env:
          EXCALIBASE_TOKEN: \${{ secrets.EXCALIBASE_TOKEN }}
          IMAGE: ${repository}@\${{ steps.build.outputs.digest }}
          COMMIT_SHA: \${{ github.sha }}
        run: |
${indent(deployScript(target), 10)}
`;
}
