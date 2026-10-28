import { describe, test, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { HostMonitoringPage } from './HostMonitoringPage';
import { FeatureGate } from '../components/FeatureGate';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';
import { alertSettings, containerSample, hostSample } from '../test/monitoringFixtures';
import type { HostSample } from '../api/monitoring';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), put: vi.fn(), post: vi.fn() } }));

interface Answers {
  readonly hostmetrics?: boolean;
  readonly current?: HostSample | null;
  readonly containers?: unknown[];
}

const containers = [
  containerSample(),
  containerSample({
    name: 'excalibase-db-shop',
    projectId: 'shop-1',
    component: 'database',
    health: 'unhealthy',
    memoryLimitBytes: null,
  }),
  containerSample({
    name: 'app-worker',
    projectId: 'shop-1',
    component: 'app',
    state: 'exited',
    health: '',
  }),
];

function serve(answers: Answers = {}) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/config') {
      return {
        data: {
          deploymentMode: 'selfhosted',
          features: { hostmetrics: answers.hostmetrics ?? true },
        },
      } as never;
    }
    if (url.startsWith('/admin/monitoring/host')) {
      const current = answers.current === undefined ? hostSample() : answers.current;
      return {
        data: { range: '1h', bucketSeconds: 60, current, points: current ? [current] : [] },
      } as never;
    }
    if (url === '/admin/monitoring/containers') {
      return {
        data: { timestamp: '2026-10-29T10:00:00Z', containers: answers.containers ?? containers },
      } as never;
    }
    if (url.startsWith('/admin/monitoring/containers/')) {
      return {
        data: { name: 'x', range: '1h', bucketSeconds: 60, points: [containerSample()] },
      } as never;
    }
    if (url === '/admin/monitoring/alert-settings') return { data: alertSettings() } as never;
    throw new Error(`unexpected GET ${url}`);
  });
}

function renderPage(role = 'platform_admin') {
  useAuthStore
    .getState()
    .setAuth({ id: 'u1', username: 'op', email: 'op@example.test', role } as never);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/admin/host']}>
        <Routes>
          <Route
            path="/admin/host"
            element={
              <FeatureGate feature="hostmetrics">
                <HostMonitoringPage />
              </FeatureGate>
            }
          />
          <Route path="/" element={<div data-testid="home" />} />
          <Route path="/project/:projectId" element={<div data-testid="project" />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const monitoringCalls = () =>
  vi
    .mocked(api.get)
    .mock.calls.map(([url]) => url as string)
    .filter((url) => url.startsWith('/admin/monitoring'));

describe('HostMonitoringPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    serve();
  });

  afterEach(() => useAuthStore.getState().clearAuth());

  test('anyone but a platform admin or operator is sent home', async () => {
    renderPage('user');
    expect(await screen.findByTestId('home')).toBeInTheDocument();
    expect(monitoringCalls()).toHaveLength(0);
  });

  test('with the feature off the page explains and calls nothing', async () => {
    serve({ hostmetrics: false });
    renderPage();
    expect(await screen.findByTestId('feature-unavailable')).toBeInTheDocument();
    expect(monitoringCalls()).toHaveLength(0);
  });

  test("shows the host's current figures", async () => {
    renderPage();
    const cards = await screen.findByTestId('host-cards');
    expect(within(cards).getByText('42.5%')).toBeInTheDocument();
    expect(within(cards).getByText('4 cores')).toBeInTheDocument();
    expect(within(cards).getByText('4.0 GB / 16.0 GB')).toBeInTheDocument();
    expect(within(cards).getByText('Swap 512.0 MB / 2.0 GB')).toBeInTheDocument();
    expect(within(cards).getByText('25.0 GB / 100.0 GB')).toBeInTheDocument();
    expect(within(cards).getByText('R 2.0 MB/s · W 512.0 KB/s')).toBeInTheDocument();
    expect(within(cards).getByText('↓ 1.0 KB/s · ↑ 3.0 MB/s')).toBeInTheDocument();
    expect(within(cards).getByText('0.52 / 0.40 / 0.33')).toBeInTheDocument();
    expect(within(cards).getByText('61.5 °C')).toBeInTheDocument();
    const sensors = screen.getByTestId('host-sensors');
    expect(within(sensors).getByText('nvme_composite')).toBeInTheDocument();
    expect(within(sensors).getByText('44.0 °C')).toBeInTheDocument();
    expect(screen.queryByText(/host interfaces not readable/)).not.toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/admin/monitoring/host?range=1h');
  });

  test('unmeasured figures read as a dash and container-summed network says so', async () => {
    serve({
      current: hostSample({
        cpuPercent: null,
        cpuCount: null,
        load1: null,
        load5: null,
        load15: null,
        memoryUsedBytes: null,
        swapTotalBytes: null,
        diskReadBytesPerSec: null,
        diskWriteBytesPerSec: null,
        networkSource: 'containers',
        temperatureMaxCelsius: null,
        temperatures: [],
      }),
    });
    renderPage();
    const cards = await screen.findByTestId('host-cards');
    expect(within(cards).getByTestId('card-cpu')).toHaveTextContent('—');
    expect(within(cards).getByText('— / 16.0 GB')).toBeInTheDocument();
    expect(within(cards).getByText('R — · W —')).toBeInTheDocument();
    expect(within(cards).getByText('— / — / —')).toBeInTheDocument();
    expect(within(cards).getByTestId('card-temperature')).toHaveTextContent('—');
    expect(screen.queryByTestId('host-sensors')).not.toBeInTheDocument();
    expect(
      screen.getByText("host interfaces not readable; showing the containers' sum"),
    ).toBeInTheDocument();
  });

  test('before the first sample the page says so', async () => {
    serve({ current: null });
    renderPage();
    expect(await screen.findByText('No host sample yet.')).toBeInTheDocument();
  });

  test('lists the containers with their state, limits and projects', async () => {
    renderPage();
    const table = await screen.findByTestId('containers-table');
    const healthy = await within(table).findByTestId('container-excalibase-provisioning');
    expect(within(healthy).getByText('platform')).toBeInTheDocument();
    expect(within(healthy).getByText('running · healthy')).toHaveAttribute('data-tone', 'ok');
    expect(within(healthy).getByText('0.25 cores')).toBeInTheDocument();
    expect(within(healthy).getByText('6.3% of host')).toBeInTheDocument();
    expect(within(healthy).getByText('256.0 MB / 1.0 GB')).toBeInTheDocument();
    expect(within(healthy).getByText('↓ 2.0 KB/s · ↑ 1.0 KB/s')).toBeInTheDocument();
    expect(within(healthy).queryByRole('link')).not.toBeInTheDocument();

    const db = within(table).getByTestId('container-excalibase-db-shop');
    expect(within(db).getByText('running · unhealthy')).toHaveAttribute('data-tone', 'bad');
    expect(within(db).getByText('256.0 MB / no limit')).toBeInTheDocument();
    expect(within(db).getByRole('link', { name: 'shop-1' })).toHaveAttribute(
      'href',
      '/project/shop-1',
    );

    const stopped = within(table).getByTestId('container-app-worker');
    expect(within(stopped).getByText('exited')).toHaveAttribute('data-tone', 'bad');
  });

  test('no containers reads as such', async () => {
    serve({ containers: [] });
    renderPage();
    expect(await screen.findByText('No containers sampled yet.')).toBeInTheDocument();
  });

  test('another range asks the server again, and a container opens its history in that range', async () => {
    renderPage();
    await screen.findByTestId('host-cards');
    await userEvent.click(screen.getByRole('button', { name: '7d' }));
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/admin/monitoring/host?range=7d'));
    expect(screen.getByRole('button', { name: '7d' })).toHaveAttribute('aria-pressed', 'true');

    const row = await screen.findByTestId('container-excalibase-db-shop');
    const toggle = within(row).getByRole('button', { name: 'History of excalibase-db-shop' });
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    await waitFor(() =>
      expect(api.get).toHaveBeenCalledWith(
        '/admin/monitoring/containers/excalibase-db-shop?range=7d',
      ),
    );
    expect(await screen.findByTestId('history-excalibase-db-shop')).toBeInTheDocument();
    await userEvent.click(toggle);
    expect(screen.queryByTestId('history-excalibase-db-shop')).not.toBeInTheDocument();
  });

  test('a failed host read says so', async () => {
    vi.mocked(api.get).mockImplementation(async (url: string) => {
      if (url === '/config')
        return { data: { deploymentMode: 'selfhosted', features: { hostmetrics: true } } } as never;
      throw new Error('boom');
    });
    renderPage();
    expect(await screen.findByText('Host metrics could not be loaded.')).toBeInTheDocument();
  });
});
