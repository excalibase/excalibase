// @vitest-environment node
import { afterEach, beforeEach, describe, expect, test } from 'vitest';
import { spawn } from 'node:child_process';
import { createServer, type IncomingMessage, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { parse } from 'yaml';
import { deployScript, githubActionsSnippet, imageRepository, type SnippetTarget } from './ciSnippets';

const DIGEST = `sha256:${'ab'.repeat(32)}`;
const COMMIT = '9fceb02d0ae598e95dc970b74767f19372d61af8';
const TOKEN = 'excali_test_token';

// A stand-in for the deploy API: it accepts one deploy and answers polls
// with the statuses it is given, in order.
interface FakeApi {
  url: string;
  deploys: Array<{ image: string; commitSha: string }>;
  polls: number;
  statuses: string[];
  refuse?: { code: number; error: string };
}

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
  fake = { url: '', deploys: [], polls: 0, statuses: ['rolling', 'succeeded'] };
  server = createServer(async (req, res) => {
    const json = (code: number, body: unknown) => {
      res.writeHead(code, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(body));
    };
    if (req.headers.authorization !== `Bearer ${TOKEN}`) return json(401, { error: 'unauthenticated' });
    if (req.method === 'POST' && req.url === '/api/projects/proj-1/apps/app-1/deploy') {
      if (fake.refuse) return json(fake.refuse.code, { error: fake.refuse.error, status: fake.refuse.code });
      fake.deploys.push(JSON.parse(await readBody(req)));
      return json(202, { id: 'dep-1', appId: 'app-1', status: 'pending', spec: { url: 'https://web.apps.test' }, url: 'https://web.apps.test' });
    }
    if (req.method === 'GET' && req.url === '/api/projects/proj-1/apps/app-1/deploys/dep-1') {
      const status = fake.statuses[Math.min(fake.polls, fake.statuses.length - 1)];
      fake.polls += 1;
      if (status === 'unavailable') {
        res.writeHead(503, { 'Content-Type': 'text/html' });
        return res.end('<html>bad gateway</html>');
      }
      return json(200, { id: 'dep-1', appId: 'app-1', status, failureReason: status === 'failed' ? 'the container never became ready' : undefined, url: 'https://web.apps.test' });
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
function run(script: string, env: Record<string, string>): Promise<{ code: number; out: string }> {
  return new Promise((resolve) => {
    const child = spawn('sh', ['-c', script], { env: { PATH: process.env.PATH ?? '', ...env } });
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

describe('deployScript', () => {
  test('deploys the digest with the commit and waits until it is live', async () => {
    const result = await run(deployScript(target()), ciEnv);
    expect(result.code, result.out).toBe(0);
    expect(fake.deploys).toEqual([{ image: `ghcr.io/acme/web@${DIGEST}`, commitSha: COMMIT }]);
    expect(fake.polls).toBe(2);
    expect(result.out).toContain('https://web.apps.test');
  }, 30_000);

  test('rides out a moment the API does not answer', async () => {
    fake.statuses = ['unavailable', 'succeeded'];
    const result = await run(deployScript(target()), ciEnv);
    expect(result.code, result.out).toBe(0);
    expect(fake.polls).toBe(2);
  }, 30_000);

  test('gives up when the API stays down', async () => {
    fake.statuses = ['unavailable'];
    const result = await run(deployScript(target()).replaceAll('sleep 5', 'sleep 0'), ciEnv);
    expect(result.code).not.toBe(0);
    expect(result.out).toContain('did not answer (503)');
    expect(fake.polls).toBe(6);
  }, 30_000);

  test('fails the job when the deploy fails, with the reason', async () => {
    fake.statuses = ['failed'];
    const result = await run(deployScript(target()), ciEnv);
    expect(result.code).not.toBe(0);
    expect(result.out).toContain('never became ready');
  }, 30_000);

  test('fails the job when the API refuses the deploy, with what it said', async () => {
    fake.refuse = { code: 422, error: 'the registry has no such image' };
    const result = await run(deployScript(target()), ciEnv);
    expect(result.code).not.toBe(0);
    expect(result.out).toContain('the registry has no such image');
    expect(fake.polls).toBe(0);
  }, 30_000);

  test('fails the job on a token the API does not accept', async () => {
    const result = await run(deployScript(target()), { ...ciEnv, EXCALIBASE_TOKEN: 'wrong' });
    expect(result.code).not.toBe(0);
    expect(fake.deploys).toHaveLength(0);
  }, 30_000);

  test('names the project and the app, never a token', () => {
    const script = deployScript(target());
    expect(script).toContain(`${fake.url}/projects/proj-1/apps/app-1`);
    expect(script).toContain('$EXCALIBASE_TOKEN');
    expect(script).not.toContain(TOKEN);
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
  test('builds with the docker actions and deploys the pushed digest with the commit', async () => {
    const steps = workflowSteps(githubActionsSnippet(target()));
    const login = steps.find((step) => step.uses?.startsWith('docker/login-action@'));
    const build = steps.find((step) => step.uses?.startsWith('docker/build-push-action@'));
    const deploy = steps.find((step) => step.run !== undefined);
    expect(login?.with?.registry).toBe('ghcr.io');
    expect(login?.with?.password).toBe('${{ secrets.GITHUB_TOKEN }}');
    expect(build?.id).toBe('build');
    expect(build?.with?.push).toBe(true);
    expect(build?.with?.tags).toBe('ghcr.io/acme/web:${{ github.sha }}');
    expect(deploy?.env).toEqual({
      EXCALIBASE_TOKEN: '${{ secrets.EXCALIBASE_TOKEN }}',
      IMAGE: 'ghcr.io/acme/web@${{ steps.build.outputs.digest }}',
      COMMIT_SHA: '${{ github.sha }}',
    });

    const result = await run(deploy?.run ?? '', ciEnv);
    expect(result.code, result.out).toBe(0);
    expect(fake.deploys).toEqual([{ image: `ghcr.io/acme/web@${DIGEST}`, commitSha: COMMIT }]);
  }, 30_000);

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
