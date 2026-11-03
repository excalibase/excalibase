import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ContainerTemplatesPage } from './ContainerTemplatesPage';
import { api } from '../api/client';
import type { AppTemplate, TemplateFit } from '../api/appTemplates';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), put: vi.fn() },
}));

const fit = (overrides: Partial<TemplateFit> = {}): TemplateFit => ({
  plan: 'FREE',
  appsNeeded: 2,
  appsHeld: 0,
  appsAllowed: 2,
  diskBytes: 1 << 30,
  diskCapBytes: 1 << 30,
  appCpu: '250m',
  appMemory: '256Mi',
  privateNetworkOn: false,
  canTurnOnPrivateNetwork: true,
  databaseReady: true,
  refusals: [],
  ...overrides,
});

const webRedis = (overrides: Partial<TemplateFit> = {}): AppTemplate => ({
  id: 'web-redis',
  format: 'excalibase.template/v1',
  name: 'Web app + Redis starter',
  summary: 'A public web app with a private Redis.',
  needsPrivateNetwork: true,
  needsDatabase: false,
  apps: [
    {
      name: 'redis',
      image: 'redis@sha256:858f',
      internal: true,
      internalPorts: [6379],
      replicas: 1,
      disk: { mountPath: '/data', size: '1Gi' },
      env: [{ name: 'REDIS_PASSWORD', source: 'generated' }],
    },
    {
      name: 'web',
      image: 'nginxinc/nginx-unprivileged@sha256:f9df',
      internal: false,
      port: 8080,
      replicas: 1,
      env: [
        { name: 'REDIS_HOST', source: 'app' },
        { name: 'GREETING', source: 'literal', value: 'hello' },
      ],
    },
  ],
  fit: fit(overrides),
});

const ACCEPTED_AT = '2026-11-04T10:00:00Z';

// What the server answers for each app the deploy created: the app and its newest deploys.
type AppReads = Record<string, { app: Record<string, unknown>; deploys: Record<string, unknown>[] }>;

function renderPage(templates: AppTemplate[], apps: AppReads = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', appHosting: true } } as never);
    if (url === '/projects/proj-1/app-templates/') return Promise.resolve({ data: templates } as never);
    for (const [id, read] of Object.entries(apps)) {
      if (url === `/projects/proj-1/apps/${id}`) return Promise.resolve({ data: read.app } as never);
      if (url === `/projects/proj-1/apps/${id}/deploys?limit=1`)
        return Promise.resolve({ data: read.deploys } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/containers/templates']}>
        <Routes>
          <Route path="/project/:projectId/containers/templates" element={<ContainerTemplatesPage />} />
          <Route path="/project/:projectId/containers" element={<p>containers list</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => vi.resetAllMocks());

const starting = (id: string, name: string) => ({ app: { id, name, status: 'PENDING' }, deploys: [] });

const deploy = (status: string, extra: Record<string, unknown> = {}) => ({
  id: 'd1', revision: 1, status, createdAt: '2026-11-04T10:00:05Z', ...extra,
});

function acceptValkey() {
  vi.mocked(api.post).mockResolvedValue({
    data: {
      templateId: 'valkey', privateNetworkTurnedOn: false, acceptedAt: ACCEPTED_AT,
      apps: [{ id: 'a1', name: 'valkey' }],
      credentials: [{ app: 'valkey', name: 'VALKEY_URL', value: 'redis://:generated@valkey:6379/0' }],
    },
  } as never);
}

async function deployValkey(read: AppReads[string]) {
  acceptValkey();
  renderPage([valkey()], { a1: read });
  await userEvent.click(await screen.findByTestId('template-deploy-valkey'));
  await userEvent.click(screen.getByTestId('template-confirm-valkey'));
  return screen.findByTestId('template-progress-valkey');
}

// The deploy answers once the apps exist; the first deploys run after it (EXC-669).
describe('ContainerTemplatesPage first deploys', () => {
  test('an app whose first deploy has not started yet shows it is starting', async () => {
    const progress = await deployValkey(starting('a1', 'valkey'));
    expect(progress).toHaveTextContent('Preparing');
  });

  test('a rolling first deploy shows it is rolling out', async () => {
    const progress = await deployValkey({ app: { id: 'a1', name: 'valkey' }, deploys: [deploy('rolling')] });
    expect(progress).toHaveTextContent('Rolling out');
  });

  test('a first deploy that succeeded shows the app is running', async () => {
    const progress = await deployValkey({ app: { id: 'a1', name: 'valkey' }, deploys: [deploy('succeeded')] });
    await waitFor(() => expect(progress).toHaveTextContent('Running'));
  });

  test('a first deploy that failed shows its reason', async () => {
    const progress = await deployValkey({
      app: { id: 'a1', name: 'valkey' },
      deploys: [deploy('failed', { failureReason: 'create the app\'s disk: timed out' })],
    });
    await waitFor(() => expect(progress).toHaveTextContent("Failed: create the app's disk: timed out"));
  });

  test('a first deploy that could not start shows what the app recorded', async () => {
    const progress = await deployValkey({
      app: {
        id: 'a1', name: 'valkey',
        lifecycleFailure: { operation: 'template deploy', reason: 'its first deploy did not start; deploy the app again', at: '2026-11-04T10:00:01Z' },
      },
      deploys: [],
    });
    await waitFor(() => expect(progress).toHaveTextContent('Failed: its first deploy did not start'));
  });

  test('an older failure recorded before this deploy is not shown as its outcome', async () => {
    const progress = await deployValkey({
      app: {
        id: 'a1', name: 'valkey',
        lifecycleFailure: { operation: 'template deploy', reason: 'old', at: '2026-11-04T09:00:00Z' },
      },
      deploys: [],
    });
    expect(progress).toHaveTextContent('Preparing');
  });
});

describe('ContainerTemplatesPage', () => {
  test('shows what a template creates and costs in plan terms', async () => {
    renderPage([webRedis()]);
    const card = await screen.findByTestId('template-web-redis');
    expect(within(card).getByText('Web app + Redis starter')).toBeInTheDocument();
    expect(within(card).getByTestId('template-cost')).toHaveTextContent('2 of 2 apps your FREE plan allows');
    expect(within(card).getByTestId('template-cost')).toHaveTextContent('250m CPU and 256Mi memory');
    expect(within(card).getByTestId('template-network')).toHaveTextContent('Turns on');
    await userEvent.click(within(card).getByTestId('template-details-web-redis'));
    const details = within(card).getByTestId('template-apps');
    expect(details).toHaveTextContent('redis');
    expect(details).toHaveTextContent('Internal service');
    expect(details).toHaveTextContent('6379');
    expect(details).toHaveTextContent('1Gi disk at /data');
    expect(details).toHaveTextContent('REDIS_PASSWORD');
    expect(details).toHaveTextContent('generated on deploy');
    expect(details).toHaveTextContent('GREETING');
  });

  test('an admin confirms the private network before the deploy', async () => {
    vi.mocked(api.post).mockResolvedValue({
      data: {
        templateId: 'web-redis',
        privateNetworkTurnedOn: true,
        acceptedAt: ACCEPTED_AT,
        apps: [
          { id: 'a1', name: 'redis' },
          { id: 'a2', name: 'web' },
        ],
      },
    } as never);
    renderPage([webRedis()], { a1: starting('a1', 'redis'), a2: starting('a2', 'web') });
    await userEvent.click(await screen.findByTestId('template-deploy-web-redis'));
    expect(api.post).not.toHaveBeenCalled();
    await userEvent.click(screen.getByTestId('template-confirm-web-redis'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/app-templates/web-redis/deploy', {
        confirmPrivateNetwork: true,
      }),
    );
    const done = await screen.findByTestId('template-deployed');
    expect(done).toHaveTextContent('redis');
    expect(done).toHaveTextContent('private network is now on');
  });

  test('a developer cannot deploy a template that turns on the network', async () => {
    renderPage([webRedis({ canTurnOnPrivateNetwork: false })]);
    const card = await screen.findByTestId('template-web-redis');
    expect(within(card).getByTestId('template-deploy-web-redis')).toBeDisabled();
    expect(within(card).getByTestId('template-network')).toHaveTextContent('org admin or owner');
  });

  test('a template the plan cannot take lists why and cannot be deployed', async () => {
    renderPage([webRedis({ appsHeld: 1, refusals: ['the template needs 2 apps and the project holds 1 of the 2 its FREE plan allows'] })]);
    const card = await screen.findByTestId('template-web-redis');
    expect(within(card).getByRole('alert')).toHaveTextContent('holds 1 of the 2');
    expect(within(card).getByTestId('template-deploy-web-redis')).toBeDisabled();
  });

  test('a refusal from the server is shown with its reasons', async () => {
    vi.mocked(api.post).mockRejectedValue({
      response: { status: 409, data: { error: 'refused', reasons: ['there is no room to run the app right now'] } },
    });
    renderPage([webRedis({ privateNetworkOn: true })]);
    await userEvent.click(await screen.findByTestId('template-deploy-web-redis'));
    await userEvent.click(screen.getByTestId('template-confirm-web-redis'));
    expect(await screen.findByText('there is no room to run the app right now')).toBeInTheDocument();
    expect(api.post).toHaveBeenCalledWith('/projects/proj-1/app-templates/web-redis/deploy', {
      confirmPrivateNetwork: false,
    });
  });
});

const valkey = (): AppTemplate => ({
  id: 'valkey',
  format: 'excalibase.template/v1',
  name: 'Valkey',
  summary: 'A Redis-compatible cache.',
  category: 'cache',
  license: 'BSD-3-Clause',
  catalog: true,
  needsPrivateNetwork: true,
  needsDatabase: false,
  apps: [
    {
      name: 'valkey',
      image: 'valkey/valkey@sha256:2d04',
      internal: true,
      internalPorts: [6379],
      replicas: 1,
      minMemory: '1Gi',
      disk: { mountPath: '/data', size: '1Gi' },
      env: [
        { name: 'VALKEY_PASSWORD', source: 'generated' },
        { name: 'VALKEY_URL', source: 'app', showOnce: true },
      ],
    },
  ],
  fit: fit({ appsNeeded: 1, privateNetworkOn: true }),
});

describe('ContainerTemplatesPage catalog', () => {
  test('a catalog service says who runs it and under which licence', async () => {
    renderPage([webRedis(), valkey()]);
    const card = await screen.findByTestId('template-valkey');
    expect(within(card).getByTestId('template-license')).toHaveTextContent('BSD-3-Clause');
    expect(within(card).getByTestId('template-category')).toHaveTextContent('Cache');
    expect(within(card).getByTestId('template-self-managed')).toHaveTextContent(
      'You run this service. Excalibase does not back it up, upgrade it or watch it.',
    );
    expect(screen.getByTestId('templates-catalog')).toContainElement(card);
    expect(within(screen.getByTestId('templates-starters')).getByTestId('template-web-redis')).toBeInTheDocument();
    await userEvent.click(within(card).getByTestId('template-details-valkey'));
    const details = within(card).getByTestId('template-apps');
    expect(details).toHaveTextContent('needs at least 1Gi memory');
    expect(details).toHaveTextContent('shown to you once');
  });

  test('the generated connection value is shown once after the deploy', async () => {
    vi.mocked(api.post).mockResolvedValue({
      data: {
        templateId: 'valkey',
        privateNetworkTurnedOn: false,
        acceptedAt: ACCEPTED_AT,
        apps: [{ id: 'a1', name: 'valkey' }],
        credentials: [{ app: 'valkey', name: 'VALKEY_URL', value: 'redis://:generated@valkey:6379/0' }],
      },
    } as never);
    renderPage([valkey()], { a1: starting('a1', 'valkey') });
    await userEvent.click(await screen.findByTestId('template-deploy-valkey'));
    await userEvent.click(screen.getByTestId('template-confirm-valkey'));
    const shown = await screen.findByTestId('template-credentials');
    expect(shown).toHaveTextContent('VALKEY_URL');
    expect(shown).toHaveTextContent('redis://:generated@valkey:6379/0');
    expect(shown).toHaveTextContent('cannot show them again');
  });

  test('a starter keeps its card as before', async () => {
    renderPage([webRedis()]);
    const card = await screen.findByTestId('template-web-redis');
    expect(within(card).queryByTestId('template-self-managed')).toBeNull();
    expect(screen.queryByTestId('templates-catalog')).toBeNull();
  });
});
