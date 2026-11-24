import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { api } from '../../../api/client';
import { DbLogsTab } from './DbLogsTab';
import { database, plane, renderTab, resetPlane, settle } from './dbTabTestKit';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

const LOGS = ['2026-10-02 LOG: checkpoint complete', '2026-10-02 ERROR: relation "x" does not exist', '2026-10-02 WARNING: slow statement'].join('\n');

function open(page: unknown = { logs: LOGS }) {
  plane.routes['/provision/p1/logs*'] = page;
  plane.routes['/provision/p1'] = database();
  return renderTab(<DbLogsTab project={database()} />);
}

const logCalls = () => vi.mocked(api.get).mock.calls.map(([url]) => String(url)).filter((url) => url.includes('/logs'));

beforeEach(resetPlane);

describe('logs tab', () => {
  test('shows the lines, colouring errors and warnings', async () => {
    open();
    expect(await screen.findByText(/relation "x" does not exist/)).toHaveClass('text-red-400');
    expect(screen.getByText(/slow statement/)).toHaveClass('text-yellow-400');
    expect(screen.getByText(/checkpoint complete/)).toHaveClass('text-text-secondary');
  });

  test('asks for the last hour first, and a wider range asks for more', async () => {
    open();
    await screen.findByText(/checkpoint/);
    expect(logCalls().at(-1)).toContain('since=1h');
    await userEvent.click(screen.getByRole('button', { name: '24h' }));
    await waitFor(() => expect(logCalls().at(-1)).toContain('since=24h'));
    expect(logCalls().at(-1)).toContain('lines=500');
    expect(screen.getByRole('button', { name: '24h' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByRole('button', { name: '1h' })).toHaveAttribute('aria-pressed', 'false');
  });

  test('typing filters the loaded lines at once and reaches the server after a pause', async () => {
    open();
    await screen.findByText(/checkpoint/);
    await userEvent.type(screen.getByRole('searchbox', { name: 'Search logs' }), 'ERROR');
    expect(screen.queryByText(/checkpoint complete/)).not.toBeInTheDocument();
    expect(screen.getByText(/relation "x"/)).toBeInTheDocument();
    await waitFor(() => expect(logCalls().at(-1)).toContain('q=ERROR'));
  });

  test('a search with no hits says so', async () => {
    open();
    await screen.findByText(/checkpoint/);
    await userEvent.type(screen.getByRole('searchbox', { name: 'Search logs' }), 'zzz');
    expect(await screen.findByText('No matching log lines')).toBeInTheDocument();
  });

  test('no logs says so', async () => {
    open({ logs: '' });
    expect(await screen.findByText('No logs available')).toBeInTheDocument();
  });

  test('a plan with short retention hides the ranges beyond it and says how long logs are kept', async () => {
    open({ logs: LOGS, retentionHours: 24 });
    expect(await screen.findByTestId('log-retention')).toHaveTextContent('kept for 1 day');
    expect(screen.getByRole('button', { name: '24h' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '7d' })).not.toBeInTheDocument();
  });

  test('logs the server cannot read show why', async () => {
    plane.failing.add('/provision/p1/logs?lines=50&since=1h');
    plane.routes['/provision/p1'] = database();
    renderTab(<DbLogsTab project={database()} />);
    expect(await screen.findByRole('alert')).toHaveTextContent('The server said no');
  });

  test('log drains are offered only with their feature on', async () => {
    plane.routes['/projects/p1/log-drains'] = [];
    open();
    await screen.findByText(/checkpoint/);
    await settle();
    expect(vi.mocked(api.get).mock.calls.some(([url]) => String(url).includes('/log-drains'))).toBe(false);
  });

  test('log drains are reachable below the lines with the feature on', async () => {
    plane.features = { logdrains: true };
    plane.routes['/projects/p1/log-drains'] = [];
    open();
    await waitFor(() => expect(vi.mocked(api.get).mock.calls.some(([url]) => String(url).includes('/log-drains'))).toBe(true));
  });
});
