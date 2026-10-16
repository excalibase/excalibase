// Copy-paste CI setups that build a frontend, pack its output folder and
// upload it to a site (EXC-353). Mirrors the server's shiptemplates.SiteCI.
import { indent, validateBuild } from '../containers/ciSnippets';

export interface SiteBuild {
  // The folder holding package.json; the repository root when empty.
  readonly context?: string;
  // The build output folder inside context; "dist" when empty.
  readonly output?: string;
  // The branch whose pushes deploy: main on GitHub, the default branch on GitLab, when empty.
  readonly branch?: string;
}

export interface SiteSnippetTarget {
  // The control-plane API, absolute, such as https://app.excalibase.io/api.
  readonly apiUrl: string;
  readonly projectId: string;
  readonly siteId: string;
  readonly build?: SiteBuild;
}

const REPO_PATH = /^[A-Za-z0-9._][A-Za-z0-9._/-]{0,199}$/;
const ARCHIVE = 'site.tgz';

// The output may not climb, quote or name the build context itself (server: validateSite).
function validateSiteBuild(build: SiteBuild = {}): void {
  validateBuild({ context: build.context, branch: build.branch });
  const output = build.output;
  if (!output) return;
  if (!REPO_PATH.test(output) || output.split('/').includes('..')) {
    throw new Error('output must be the build output folder inside the build context, e.g. dist');
  }
  if (cleanJoin(output) === '') {
    throw new Error('output must be the build output folder, not the build context itself');
  }
}

// path.Join for paths already free of "..": drops empty and "." segments.
const cleanJoin = (...parts: string[]) =>
  parts
    .flatMap((part) => part.split('/'))
    .filter((segment) => segment !== '' && segment !== '.')
    .join('/');

const siteOutput = (build: SiteBuild = {}) =>
  cleanJoin(build.context ?? '', build.output || 'dist');

const deployUrl = ({ apiUrl, projectId, siteId }: SiteSnippetTarget) =>
  `${apiUrl.replace(/\/+$/, '')}/projects/${projectId}/sites/${siteId}/deploy`;

// The API answers once the deploy is live, and an error status (a failed step) when it is not.
function upload(target: SiteSnippetTarget, commitVar: string): string {
  validateSiteBuild(target.build);
  return `tar czf ${ARCHIVE} -C "${siteOutput(target.build)}" .
curl --fail-with-body -sS -X POST "${deployUrl(target)}?label=${commitVar}" \\
  -H "Authorization: Bearer $EXCALIBASE_TOKEN" \\
  -H "Content-Type: application/gzip" \\
  --data-binary @${ARCHIVE}`;
}

// Builds in the context and returns to the repository root at rootVar.
function shellBuild(build: SiteBuild = {}, rootVar: string): string[] {
  if (!build.context) return ['npm ci', 'npm run build'];
  return [`cd "${build.context}"`, 'npm ci', 'npm run build', `cd "${rootVar}"`];
}

export function siteGithubActionsSnippet(target: SiteSnippetTarget): string {
  const deploy = upload(target, '$GITHUB_SHA');
  const branch = target.build?.branch || 'main';
  const workingDirectory = target.build?.context
    ? `\n        working-directory: "${target.build.context}"`
    : '';
  return `name: Deploy site to Excalibase
on:
  push:
    branches: ["${branch}"]
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
      - name: Build${workingDirectory}
        run: |
          npm ci
          npm run build
      - name: Deploy
        env:
          EXCALIBASE_TOKEN: \${{ secrets.EXCALIBASE_TOKEN }}
        run: |
${indent(deploy, 10)}
`;
}

export function siteGitlabCiSnippet(target: SiteSnippetTarget): string {
  const [pack, ...curl] = upload(target, '$CI_COMMIT_SHA').split('\n');
  const branch = target.build?.branch ? `"${target.build.branch}"` : '$CI_DEFAULT_BRANCH';
  const script = [...shellBuild(target.build, '$CI_PROJECT_DIR'), pack]
    .map((line) => `    - ${line}\n`)
    .join('');
  return `deploy-site:
  stage: deploy
  image: node:24
  resource_group: excalibase-site-deploy
  rules:
    - if: $CI_COMMIT_BRANCH == ${branch}
  script:
${script}    - |
${indent(curl.join('\n'), 6)}
`;
}

// Needs a Node-capable agent and a secret-text credential "excalibase-token";
// backslashes are doubled for Groovy's triple-quoted strings.
export function siteJenkinsSnippet(target: SiteSnippetTarget): string {
  const script = [...shellBuild(target.build, '$WORKSPACE'), upload(target, '$GIT_COMMIT')].join(
    '\n',
  );
  const branch = target.build?.branch;
  const when = branch
    ? `\n      when { anyOf { branch '${branch}'; expression { env.GIT_BRANCH == 'origin/${branch}' } } }`
    : '';
  return `pipeline {
  agent any
  options {
    disableConcurrentBuilds()
  }
  environment {
    EXCALIBASE_TOKEN = credentials('excalibase-token')
  }
  stages {
    stage('Build and deploy the site') {${when}
      steps {
        sh '''
${indent(script.replaceAll('\\', '\\\\'), 10)}
        '''
      }
    }
  }
}
`;
}

export function siteCurlSnippet(target: SiteSnippetTarget): string {
  return `# Run from the repository root after building the site, with these set:
#   EXCALIBASE_TOKEN  a write token bound to this project, kept as a CI secret
#   COMMIT_SHA        the commit the build came from, shown in the deploy history
# It answers once the deploy is live, and fails the step when it is not.
${upload(target, '$COMMIT_SHA')}
`;
}
