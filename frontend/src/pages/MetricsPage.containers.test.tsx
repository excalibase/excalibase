import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { MetricsPage } from './MetricsPage';
import { api } from '../api/client';
import { containerSample } from '../test/monitoringFixtures';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const METRICS = {
  projectId: 'p-1',
  timestamp: '2026-10-29T10:00:00Z',
  status: 'ACTIVE',
  healthStatus: 'HEALTHY',
  metricsAvailable: true,
  unavailableReason: null,
  cpuUsagePercent: 10,
  memoryUsagePercent: 20,
  diskUsagePercent: null,
  cpuUsageCores: 0.1,
  memoryUsageMB: 128,
  diskUsageGB: null,
  activeConnections: 3,
  idleConnections: 2,
  maxConnections: 100,
  cpuLimitCores: 0.5,
  memoryLimitMB: 512,
  storageLimit: '5Gi',
  instanceCount: 1,
};

const db = containerSample({ name: 'excalibase-db-p-1', projectId: 'p-1', component: 'database' });
const app = containerSample({
  name: 'p-1-web',
  projectId: 'p-1',
  component: 'app',
  state: 'exited',
  health: '',
  memoryLimitBytes: null,
});

function serve(
  hostmetrics: boolean,
  containers: unknown[] = [
    {
      name: db.name,
      component: 'database',
      state: 'running',
      health: 'healthy',
      current: db,
      points: [db, { ...db, cpuPercent: null }],
    },
    { name: app.name, component: 'app', state: 'exited', health: '', current: null, points: [] },
  ],
) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/config')
      return { data: { deploymentMode: 'selfhosted', features: { hostmetrics } } } as never;
    if (url.includes('/metrics/current')) return { data: METRICS } as never;
    if (url.includes('/metrics/history'))
      return { data: { projectId: 'p-1', metrics: [METRICS], totalPoints: 1 } } as never;
    if (url.startsWith('/projects/p-1/containers/metrics')) {
      return { data: { range: '1h', bucketSeconds: 60, containers } } as never;
    }
    return { data: [] } as never;
  });
}

function renderPage() {
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <MemoryRouter initialEntries={['/project/p-1/monitoring/metrics']}>
        <Routes>
          <Route path="/project/:projectId/monitoring/metrics" element={<MetricsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const containerCalls = () =>
  vi
    .mocked(api.get)
    .mock.calls.map(([url]) => url as string)
    .filter((url) => url.includes('/containers/metrics'));

describe('MetricsPage containers', () => {
  beforeEach(() => vi.clearAllMocks());

  test("with host metrics on, lists the project's containers with their current figures", async () => {
    serve(true);
    renderPage();
    const section = await screen.findByTestId('project-containers');
    const dbCard = await within(section).findByTestId('project-container-excalibase-db-p-1');
    expect(within(dbCard).getByText('database')).toBeInTheDocument();
    expect(within(dbCard).getByText('running · healthy')).toHaveAttribute('data-tone', 'ok');
    expect(within(dbCard).getByText('0.25 cores')).toBeInTheDocument();
    expect(within(dbCard).getByText('256.0 MB / 1.0 GB')).toBeInTheDocument();
    const appCard = within(section).getByTestId('project-container-p-1-web');
    expect(within(appCard).getByText('exited')).toHaveAttribute('data-tone', 'bad');
    expect(within(appCard).getByText('No samples in this range.')).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/projects/p-1/containers/metrics?range=1h');
  });

  test('another range asks the server again', async () => {
    serve(true);
    renderPage();
    const section = await screen.findByTestId('project-containers');
    await userEvent.click(within(section).getByRole('button', { name: '30d' }));
    await waitFor(() =>
      expect(api.get).toHaveBeenCalledWith('/projects/p-1/containers/metrics?range=30d'),
    );
  });

  test('a project with no containers says so', async () => {
    serve(true, []);
    renderPage();
    expect(await screen.findByText('No containers for this project.')).toBeInTheDocument();
  });

  test('with host metrics off, the page is as before and asks nothing about containers', async () => {
    serve(false);
    renderPage();
    expect(await screen.findByText('CPU')).toBeInTheDocument();
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByTestId('project-containers')).not.toBeInTheDocument();
    expect(containerCalls()).toHaveLength(0);
  });
});
