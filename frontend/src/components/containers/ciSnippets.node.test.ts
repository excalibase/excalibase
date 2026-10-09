// @vitest-environment node
import { afterEach, beforeEach, describe, expect, test } from 'vitest';
import { spawn } from 'node:child_process';
import { chmodSync, existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { createServer, type IncomingMessage, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { parse } from 'yaml';
import {
  curlSnippet,
  deployCommand,
  githubActionsSnippet,
  gitlabCiSnippet,
  imageRepository,
  jenkinsSnippet,
  type SnippetTarget,
} from './ciSnippets';

const DIGEST = `sha256:${'ab'.repeat(32)}`;
const COMMIT = '9fceb02d0ae598e95dc970b74767f19372d61af8';
const TOKEN = 'excali_test_token';

// A stand-in for the deploy API: it accepts one deploy and, asked to wait,
// answers with how it ended, the way the server does.
interface FakeApi {
  url: string;
  deploys: Array<{ image: string; commitSha: string }>;
  waited: number;
  outcome: 'succeeded' | 'failed' | 'superseded';
  refuse?: { code: number; error: string };
}

const WAIT_STATUS = { succeeded: 200, failed: 422, superseded: 409 } as const;

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    let body = '';
    req.on('data', (chunk) => (body += chunk));
    req.on('end', () => resolve(body));
  });
}

let server: Server;
let fake: FakeApi;

beforeEach(async () => {
  fake = { url: '', deploys: [], waited: 0, outcome: 'succeeded' };
  server = createServer(async (req, res) => {
    const json = (code: number, body: unknown) => {
      res.writeHead(code, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(body));
    };
    if (req.headers.authorization !== `Bearer ${TOKEN}`) return json(401, { error: 'unauthenticated' });
    const [path, query] = (req.url ?? '').split('?');
    if (req.method === 'POST' && path === '/api/projects/proj-1/apps/app-1/deploy') {
      if (fake.refuse) return json(fake.refuse.code, { error: fake.refuse.error, status: fake.refuse.code });
      fake.deploys.push(JSON.parse(await readBody(req)));
      const deploy = { id: 'dep-1', appId: 'app-1', url: 'https://web.apps.test' };
      if (query !== 'wait=true') return json(202, { ...deploy, status: 'pending' });
      fake.waited += 1;
      const failed = fake.outcome === 'failed' ? { failureReason: 'the container never became ready', error: 'deploy dep-1 failed: the container never became ready' } : {};
      return json(WAIT_STATUS[fake.outcome], { ...deploy, status: fake.outcome, ...failed });
    }
    return json(404, { error: 'not found' });
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  fake.url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/api`;
});

afterEach(() => new Promise<void>((resolve) => server.close(() => resolve())));

function target(image = 'ghcr.io/acme/web:main'): SnippetTarget {
  return { apiUrl: fake.url, projectId: 'proj-1', appId: 'app-1', image };
}

// Runs a script the way a CI runner does, with POSIX sh, and reports how it ended.
function run(script: string, env: Record<string, string>, shell = ['sh']): Promise<{ code: number; out: string }> {
  return new Promise((resolve) => {
    const child = spawn(shell[0], [...shell.slice(1), '-c', script], { env: { PATH: process.env.PATH ?? '', ...env } });
    let out = '';
    child.stdout.on('data', (chunk) => (out += chunk));
    child.stderr.on('data', (chunk) => (out += chunk));
    child.on('close', (code) => resolve({ code: code ?? -1, out }));
  });
}

const ciEnv = { EXCALIBASE_TOKEN: TOKEN, IMAGE: `ghcr.io/acme/web@${DIGEST}`, COMMIT_SHA: COMMIT };

describe('imageRepository', () => {
  test.each([
    ['ghcr.io/acme/web:main', 'ghcr.io/acme/web'],
    [`ghcr.io/acme/web@${DIGEST}`, 'ghcr.io/acme/web'],
    ['localhost:5000/web:1', 'localhost:5000/web'],
    ['nginx:1.27', 'nginx'],
    ['registry.example.com:8443/a/b:v1', 'registry.example.com:8443/a/b'],
  ])('%s → %s', (image, repository) => {
    expect(imageRepository(image)).toBe(repository);
  });
});

describe('deployCommand', () => {
  const command = () => deployCommand(target(), '$COMMIT_SHA');

  test('deploys the digest with the commit and answers once it is live', async () => {
    const result = await run(command(), ciEnv);
    expect(result.code, result.out).toBe(0);
    expect(fake.deploys).toEqual([{ image: `ghcr.io/acme/web@${DIGEST}`, commitSha: COMMIT }]);
    expect(fake.waited).toBe(1);
    expect(result.out).toContain('https://web.apps.test');
  }, 30_000);

  test.each(['failed', 'superseded'] as const)('fails the job when the deploy is %s, with what the API said', async (outcome) => {
    fake.outcome = outcome;
    const result = await run(command(), ciEnv);
    expect(result.code).not.toBe(0);
    expect(result.out).toContain(`"status":"${outcome}"`);
  }, 30_000);

  test('fails the job when the API refuses the deploy, with what it said', async () => {
    fake.refuse = { code: 422, error: 'the registry has no such image' };
    const result = await run(command(), ciEnv);
    expect(result.code).not.toBe(0);
    expect(result.out).toContain('the registry has no such image');
  }, 30_000);

  test('fails the job on a token the API does not accept', async () => {
    const result = await run(command(), { ...ciEnv, EXCALIBASE_TOKEN: 'wrong' });
    expect(result.code).not.toBe(0);
    expect(fake.deploys).toHaveLength(0);
  }, 30_000);

  test('is one command naming the project and the app, never a token', () => {
    const text = command();
    expect(text).toContain(`${fake.url}/projects/proj-1/apps/app-1/deploy?wait=true`);
    expect(text).toContain('$EXCALIBASE_TOKEN');
    expect(text).not.toContain(TOKEN);
    expect(text.split('\n').every((line, index, lines) => index === lines.length - 1 || line.endsWith('\\'))).toBe(true);
  });
});

interface Step {
  id?: string;
  name?: string;
  uses?: string;
  with?: Record<string, string>;
  env?: Record<string, string>;
  run?: string;
}

function workflowSteps(yamlText: string): Step[] {
  const workflow = parse(yamlText) as { jobs: Record<string, { steps: Step[] }> };
  return Object.values(workflow.jobs)[0].steps;
}

describe('githubActionsSnippet', () => {
  test('builds with the docker actions and deploys the pushed digest with the deploy action', () => {
    const steps = workflowSteps(githubActionsSnippet(target()));
    const login = steps.find((step) => step.uses?.startsWith('docker/login-action@'));
    const build = steps.find((step) => step.uses?.startsWith('docker/build-push-action@'));
    const deploy = steps.find((step) => step.uses === 'excalibase/deploy-action@v1');
    expect(login?.with?.registry).toBe('ghcr.io');
    expect(login?.with?.password).toBe('${{ secrets.GITHUB_TOKEN }}');
    expect(build?.id).toBe('build');
    expect(build?.with?.push).toBe(true);
    expect(build?.with?.tags).toBe('ghcr.io/acme/web:main,ghcr.io/acme/web:${{ github.sha }}');
    expect(build?.with?.context).toBe('.');
    expect(deploy?.with).toEqual({
      app: 'proj-1/app-1',
      image: 'ghcr.io/acme/web@${{ steps.build.outputs.digest }}',
      token: '${{ secrets.EXCALIBASE_TOKEN }}',
      'api-url': fake.url,
    });
  });

  test('leaves out api-url when it is the action default', () => {
    const snippet = githubActionsSnippet({ ...target(), apiUrl: 'https://app.excalibase.io/api/' });
    expect(snippet).not.toContain('api-url');
    expect(snippet.trim().split('\n').length).toBeLessThanOrEqual(30);
  });

  test('refuses build options that leave the repository or break out of the pipeline', () => {
    const unsafe = [
      { context: '../x' }, { dockerfile: '/etc/passwd' }, { context: "a'b" },
      { branch: '${{ github.event }}' }, { branch: 'a..b' },
    ];
    for (const build of unsafe) {
      for (const render of [githubActionsSnippet, gitlabCiSnippet, jenkinsSnippet]) {
        expect(() => render({ ...target(), build })).toThrow();
      }
    }
  });

  test('logs in to Docker Hub with its own secrets', () => {
    const steps = workflowSteps(githubActionsSnippet(target('acme/web:main')));
    const login = steps.find((step) => step.uses?.startsWith('docker/login-action@'));
    expect(login?.with?.registry).toBeUndefined();
    expect(login?.with?.username).toBe('${{ secrets.DOCKERHUB_USERNAME }}');
    expect(login?.with?.password).toBe('${{ secrets.DOCKERHUB_TOKEN }}');
  });

  test('logs in to any other registry with named secrets', () => {
    const steps = workflowSteps(githubActionsSnippet(target('registry.example.com/acme/web:main')));
    const login = steps.find((step) => step.uses?.startsWith('docker/login-action@'));
    expect(login?.with).toEqual({
      registry: 'registry.example.com',
      username: '${{ secrets.REGISTRY_USERNAME }}',
      password: '${{ secrets.REGISTRY_PASSWORD }}',
    });
  });
});

// A docker stand-in on PATH: it records each call and answers inspect the way
// docker does after a push, with the repository at the pushed digest.
function fakeTools(): { bin: string; calls: () => string[] } {
  const dir = mkdtempSync(join(tmpdir(), 'ci-snippet-'));
  const log = join(dir, 'calls.log');
  writeFileSync(
    join(dir, 'docker'),
    `#!/bin/sh
if [ "$1" = login ]; then cat >/dev/null; fi
echo "docker $*" >> "${log}"
if [ "$1" = inspect ]; then
  for last in "$@"; do :; done
  echo "elsewhere.example.com/other/repo@sha256:${'0'.repeat(64)}"
  echo "\${last%:*}@${DIGEST}"
fi
`,
  );
  writeFileSync(join(dir, 'apk'), `#!/bin/sh\necho "apk $*" >> "${log}"\n`);
  chmodSync(join(dir, 'docker'), 0o755);
  chmodSync(join(dir, 'apk'), 0o755);
  return {
    bin: dir,
    calls: () => (existsSync(log) ? readFileSync(log, 'utf8').trim().split('\n') : []),
  };
}

describe('gitlabCiSnippet', () => {
  test('builds, pushes and deploys the pushed digest with the commit', async () => {
    const yamlText = gitlabCiSnippet(target('registry.gitlab.com/acme/web:main'));
    const job = (parse(yamlText) as Record<string, { script: string[]; image: string; services: string[]; variables: Record<string, string> }>)['deploy'];
    expect(job.image).toMatch(/^docker:/);
    expect(job.services[0]).toMatch(/^docker:.*dind$/);
    const tools = fakeTools();

    const result = await run(job.script.join('\n'), {
      ...job.variables,
      PATH: `${tools.bin}:${process.env.PATH}`,
      EXCALIBASE_TOKEN: TOKEN,
      CI_COMMIT_SHA: COMMIT,
      CI_REGISTRY: 'registry.gitlab.com',
      CI_REGISTRY_USER: 'gitlab-ci-token',
      CI_REGISTRY_PASSWORD: 'job-token',
    });
    expect(result.code, result.out).toBe(0);
    expect(tools.calls()).toEqual(
      expect.arrayContaining([
        'docker login -u gitlab-ci-token --password-stdin registry.gitlab.com',
        `docker build -t registry.gitlab.com/acme/web:${COMMIT} .`,
        `docker push registry.gitlab.com/acme/web:${COMMIT}`,
      ]),
    );
    expect(fake.deploys).toEqual([{ image: `registry.gitlab.com/acme/web@${DIGEST}`, commitSha: COMMIT }]);
  }, 30_000);

  // GitLab's docker image runs the job in busybox ash, not dash.
  test.skipIf(!existsSync('/usr/bin/busybox'))('runs under busybox ash as in the docker image', async () => {
    const job = (parse(gitlabCiSnippet(target('registry.gitlab.com/acme/web:main'))) as Record<string, { script: string[]; variables: Record<string, string> }>)['deploy'];
    const tools = fakeTools();
    const result = await run(
      job.script.join('\n'),
      { ...job.variables, PATH: `${tools.bin}:${process.env.PATH}`, EXCALIBASE_TOKEN: TOKEN, CI_COMMIT_SHA: COMMIT },
      ['/usr/bin/busybox', 'sh'],
    );
    expect(result.code, result.out).toBe(0);
    expect(fake.deploys).toEqual([{ image: `registry.gitlab.com/acme/web@${DIGEST}`, commitSha: COMMIT }]);
  }, 30_000);

  test('logs in to another registry with masked variables', () => {
    const yamlText = gitlabCiSnippet(target('ghcr.io/acme/web:main'));
    expect(yamlText).toContain('"$REGISTRY_PASSWORD" | docker login -u "$REGISTRY_USERNAME" --password-stdin ghcr.io');
    expect(gitlabCiSnippet(target('acme/web:main'))).toContain(
      '"$DOCKERHUB_TOKEN" | docker login -u "$DOCKERHUB_USERNAME" --password-stdin',
    );
  });
});

// Groovy reads a ''' string's backslash escapes; this is what sh then receives.
function groovySingleQuoted(text: string): string {
  return text.replace(/\\(\n|.)/g, (_, next: string) => {
    const escapes: Record<string, string> = { '\\': '\\', "'": "'", n: '\n', t: '\t', $: '$', '\n': '' };
    if (!(next in escapes)) throw new Error(`Groovy refuses the escape \\${next}`);
    return escapes[next];
  });
}

function jenkinsShellSteps(jenkinsfile: string): string[] {
  return [...jenkinsfile.matchAll(/sh '''([\s\S]*?)'''/g)].map((match) => groovySingleQuoted(match[1]));
}

describe('jenkinsSnippet', () => {
  test('builds, pushes and deploys the pushed digest with the commit', async () => {
    const jenkinsfile = jenkinsSnippet(target());
    expect(jenkinsfile).toContain("EXCALIBASE_TOKEN = credentials('excalibase-token')");
    const steps = jenkinsShellSteps(jenkinsfile);
    expect(steps).toHaveLength(1);
    const tools = fakeTools();
    const env = {
      PATH: `${tools.bin}:${process.env.PATH}`,
      IMAGE_REPOSITORY: 'ghcr.io/acme/web',
      GIT_COMMIT: COMMIT,
      EXCALIBASE_TOKEN: TOKEN,
      REGISTRY_USERNAME: 'ci',
      REGISTRY_PASSWORD: 'secret',
    };

    for (const step of steps) {
      const result = await run(step, env);
      expect(result.code, result.out).toBe(0);
    }
    expect(tools.calls()).toEqual(
      expect.arrayContaining([
        'docker login -u ci --password-stdin ghcr.io',
        `docker push ghcr.io/acme/web:${COMMIT}`,
      ]),
    );
    expect(fake.deploys).toEqual([{ image: `ghcr.io/acme/web@${DIGEST}`, commitSha: COMMIT }]);
  }, 30_000);

  test('names the repository it pushes to', () => {
    expect(jenkinsSnippet(target('acme/web:main'))).toContain("IMAGE_REPOSITORY = 'acme/web'");
  });
});

describe('curlSnippet', () => {
  test('deploys from any CI that sets the three variables', async () => {
    const result = await run(curlSnippet(target()), ciEnv);
    expect(result.code, result.out).toBe(0);
    expect(fake.deploys).toEqual([{ image: `ghcr.io/acme/web@${DIGEST}`, commitSha: COMMIT }]);
  }, 30_000);

  test('says what it needs before it runs', () => {
    const snippet = curlSnippet(target());
    for (const name of ['EXCALIBASE_TOKEN', 'IMAGE', 'COMMIT_SHA']) expect(snippet).toContain(name);
  });
});
