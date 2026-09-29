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

function renderPage(templates: AppTemplate[]) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', appHosting: true } } as never);
    if (url === '/projects/proj-1/app-templates/') return Promise.resolve({ data: templates } as never);
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
        apps: [
          { id: 'a1', name: 'redis', deployId: 'd1', deployStatus: 'rolling' },
          { id: 'a2', name: 'web', deployId: 'd2', deployStatus: 'rolling' },
        ],
      },
    } as never);
    renderPage([webRedis()]);
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
