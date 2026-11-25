import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen, within } from '@testing-library/react';
import { DbMetricsTab } from './DbMetricsTab';
import { calledWith, database, plane, renderTab, resetPlane, settle } from './dbTabTestKit';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));
// The chart measures its box, which jsdom does not have.
vi.mock('recharts', async (importOriginal) => {
  const original = await importOriginal<typeof import('recharts')>();
  return { ...original, ResponsiveContainer: ({ children }: { children: React.ReactNode }) => <div data-testid="chart-box">{children}</div> };
});

const CURRENT = {
  metricsAvailable: true,
  unavailableReason: null,
  healthStatus: 'HEALTHY',
  cpuUsageCores: 0.25,
  cpuLimitCores: 2,
  instanceCount: 2,
  memoryUsageMB: 512,
  memoryLimitMB: 2048,
  storageLimit: '10Gi',
  databaseSizeBytes: 2048,
  activeConnections: 3,
  maxConnections: 50,
  lastBackupTime: '2026-10-02T03:00:00Z',
  pods: [
    { name: 'db-1', role: 'primary', cpuCores: 0.2, cpuLimitCores: 1, memoryMB: 300, memoryLimitMB: 1024 },
    { name: 'db-2', role: 'replica', cpuCores: 0.05, cpuLimitCores: 1, memoryMB: 212, memoryLimitMB: 1024 },
  ],
};
const HISTORY = {
  metrics: [
    { metricsAvailable: true, timestamp: '2026-10-02T01:00:00Z', cpuUsagePercent: 10, memoryUsagePercent: 20 },
    { metricsAvailable: true, timestamp: '2026-10-02T01:01:00Z', cpuUsagePercent: 30, memoryUsagePercent: 25 },
  ],
};

function open(current: unknown = CURRENT, history: unknown = HISTORY) {
  plane.routes['/provision/p1/metrics/current'] = current;
  plane.routes['/provision/p1/metrics/history*'] = history;
  plane.routes['/provision/p1'] = database();
  return renderTab(<DbMetricsTab project={database()} />);
}

beforeEach(resetPlane);

describe('metrics tab', () => {
  test('reads usage against the limits', async () => {
    open();
    expect(await screen.findByText('0.25 cores')).toBeInTheDocument();
    expect(screen.getByText('/ 2 limit (2 instances)')).toBeInTheDocument();
    expect(screen.getByText('512 MB')).toBeInTheDocument();
    expect(screen.getByText('3 of 50')).toBeInTheDocument();
    expect(screen.getByText('Healthy')).toBeInTheDocument();
    expect(screen.getByText('10Gi')).toBeInTheDocument();
  });

  test('lists each instance with its role and use', async () => {
    open();
    const primary = await screen.findByTestId('instance-db-1');
    expect(within(primary).getByText('primary')).toBeInTheDocument();
    expect(primary).toHaveTextContent('Memory 300 / 1024 MB');
    expect(screen.getByTestId('instance-db-2')).toHaveTextContent('replica');
  });

  test('draws the trend once there is history', async () => {
    open();
    expect(await screen.findByRole('heading', { name: 'Usage over time' })).toBeInTheDocument();
    expect(screen.getByTestId('chart-box')).toBeInTheDocument();
  });

  test('a new project with no history draws no trend', async () => {
    open(CURRENT, { metrics: null });
    await screen.findByText('0.25 cores');
    await settle();
    expect(screen.queryByRole('heading', { name: 'Usage over time' })).not.toBeInTheDocument();
  });

  test('a flat zero history draws no trend', async () => {
    open(CURRENT, { metrics: HISTORY.metrics.map((m) => ({ ...m, cpuUsagePercent: 0, memoryUsagePercent: 0 })) });
    await screen.findByText('0.25 cores');
    await settle();
    expect(screen.queryByRole('heading', { name: 'Usage over time' })).not.toBeInTheDocument();
  });

  test('an unmeasured value says so', async () => {
    open({ ...CURRENT, cpuUsageCores: null, memoryUsageMB: null, lastBackupTime: undefined, pods: undefined });
    expect(await screen.findAllByText('Not available')).toHaveLength(2);
    expect(screen.getByText('None')).toBeInTheDocument();
    expect(screen.queryByTestId('instance-db-1')).not.toBeInTheDocument();
  });

  test('metrics that cannot be read say why, without setup instructions', async () => {
    open({ metricsAvailable: false, unavailableReason: 'The database is not reachable' });
    expect(await screen.findByText('The database is not reachable.')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Metrics unavailable' })).toBeInTheDocument();
    expect(screen.queryByText(/9187|metricsEnabled/)).not.toBeInTheDocument();
  });

  test('a single host with monitoring off says metrics are not on, as a plain sentence and with no Kubernetes', async () => {
    open({ metricsAvailable: false, unavailableReason: 'database metrics are not turned on for this single host: switch on host monitoring to see them' });
    const note = await screen.findByText(/^Database metrics are not turned on for this single host/);
    expect(note).toHaveTextContent(/to see them\.$/);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(note.textContent).not.toMatch(/kubernetes/i);
  });

  test('a project that has no metrics yet says so', async () => {
    plane.routes['/provision/p1/metrics/current'] = null;
    renderTab(<DbMetricsTab project={database()} />);
    expect(await screen.findByText('No metrics yet.')).toBeInTheDocument();
  });

  test('the container section and the endpoint card stay off without their features', async () => {
    open();
    await screen.findByText('0.25 cores');
    await settle();
    expect(calledWith('/containers/metrics')).toBe(false);
    expect(calledWith('/metrics-keys')).toBe(false);
  });

  test('the container section and the endpoint card appear with their features', async () => {
    plane.features = { hostmetrics: true, metricsendpoint: true };
    plane.routes['/projects/p1/metrics-keys'] = [];
    open();
    await screen.findByText('0.25 cores');
    await settle();
    expect(calledWith('/containers/metrics')).toBe(true);
    expect(calledWith('/metrics-keys')).toBe(true);
  });
});
