import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { InstanceDetailPage } from './InstanceDetailPage';
import { MetricsPage } from './MetricsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const PROJECT = {
  projectId: 'p-1', databaseType: 'POSTGRESQL', tier: 'FREE', status: 'ACTIVE', currentStage: 'COMPLETED',
  namespace: 'ns', host: 'h', port: '5432', databaseName: 'app', createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z',
};

// What prod answered (EXC-633): the database figures arrive, the usage ones do not.
const METRICS = {
  projectId: 'p-1', timestamp: '2026-10-18T19:30:14Z', status: 'ACTIVE', healthStatus: 'HEALTHY',
  metricsAvailable: true, unavailableReason: null,
  cpuUsagePercent: null, memoryUsagePercent: null, diskUsagePercent: null,
  cpuUsageCores: null, memoryUsageMB: null, diskUsageGB: null,
  activeConnections: 3, idleConnections: 2, maxConnections: 100,
  cpuLimitCores: 0.5, memoryLimitMB: 512, storageLimit: '5Gi', instanceCount: 1,
};

function serve(logs: () => unknown, metrics: object = METRICS) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/provision/p-1') return { data: PROJECT } as never;
    if (url.includes('/metrics/current')) return { data: metrics } as never;
    if (url.includes('/metrics/history')) return { data: { projectId: 'p-1', metrics: [METRICS], totalPoints: 1 } } as never;
    if (url.includes('/logs')) return logs() as never;
    return { data: [] } as never;
  });
}

function renderAt(path: string, element: React.ReactElement) {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={[`/project/p-1/${path}`]}>
        <Routes>
          <Route path={`/project/:projectId/${path}`} element={element} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('Studio monitoring says what it cannot measure', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the project overview names CPU, memory and disk as not available', async () => {
    serve(() => ({ data: { logs: '' } }));
    renderAt('database/overview', <InstanceDetailPage />);
    for (const label of ['CPU', 'Memory', 'Disk']) {
      const card = (await screen.findByText(label, { selector: 'p,span,div' })).closest('div')!.parentElement!;
      expect(within(card).getAllByText('Not available').length).toBeGreaterThan(0);
    }
  });

  test('the metrics page names CPU and memory as not available', async () => {
    serve(() => ({ data: { logs: '' } }));
    renderAt('monitoring/metrics', <MetricsPage />);
    expect(await screen.findAllByText('Not available')).toHaveLength(2);
  });

  test("the overview's log tab shows why the logs could not be read", async () => {
    serve(() => Promise.reject({
      message: 'Request failed with status code 503',
      response: { status: 503, data: { error: "the database's logs are not available right now", status: 503 } },
    }));
    renderAt('database/overview', <InstanceDetailPage />);
    await userEvent.click(await screen.findByRole('button', { name: /^logs$/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent("the database's logs are not available right now");
    expect(screen.queryByText(/Loading logs/)).not.toBeInTheDocument();
  });

  test("the overview's log tab shows the database's log", async () => {
    serve(() => ({ data: { logs: 'checkpoint complete\n' } }));
    renderAt('database/overview', <InstanceDetailPage />);
    await userEvent.click(await screen.findByRole('button', { name: /^logs$/i }));
    expect(await screen.findByText(/checkpoint complete/)).toBeInTheDocument();
  });

  test('measured figures show as numbers', async () => {
    serve(() => ({ data: { logs: '' } }), {
      ...METRICS, cpuUsagePercent: 12.4, memoryUsagePercent: 40, diskUsagePercent: 13,
      cpuUsageCores: 0.06, memoryUsageMB: 205, diskUsageGB: 1,
    });
    renderAt('database/overview', <InstanceDetailPage />);
    expect(await screen.findByText('12%')).toBeInTheDocument();
    expect(screen.getByText('205 MB')).toBeInTheDocument();
    expect(screen.getByText('1 GB')).toBeInTheDocument();
    expect(screen.queryByText('Not available')).not.toBeInTheDocument();
  });
});
