import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { api } from '../../../api/client';
import { useAuthStore } from '../../../stores/auth-store';
import { ServicePanel } from './ServicePanel';
import { tabsFor, type ServiceRef } from './panelState';
import type { DatabaseInstance } from '../../../types';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

const database = (extra: Partial<DatabaseInstance> = {}) =>
  ({ projectId: 'p1', orgId: 'org-1', databaseType: 'POSTGRESQL', postgresVersion: '17', tier: 'FREE', status: 'ACTIVE', currentStage: 'COMPLETED', ...extra }) as DatabaseInstance;

const APP = { id: 'api', projectId: 'p1', name: 'api', image: 'ghcr.io/me/api:1', env: [{ name: 'LOG', kind: 'literal', value: 'x' }], port: 8080, replicas: 1, tier: 'FREE', status: 'ACTIVE', version: 1, url: 'https://api.example.app', internalPorts: [] };
const SITE = { id: 'web', projectId: 'p1', name: 'web', hostname: 'web.excalibase.app', url: 'https://web.excalibase.app', spaFallback: false, liveDeployId: 'd1' };

let orgRole = 'owner';

function route(url: string) {
  const ok = (data: unknown) => Promise.resolve({ data });
  if (url === '/orgs') return ok([{ id: 'org-1', name: 'Acme', role: orgRole }]);
  if (url === '/config') return ok({ deploymentMode: 'cloud', appHosting: true, sites: true, features: { studio_v2: true } });
  if (url === '/tiers') return ok([{ tier: 'FREE', cpu: '1', memory: '2Gi', autoPauseAfterDays: 7 }]);
  if (url === '/provision/p1') return ok(database());
  if (url.endsWith('/metrics/current')) return ok({ activeConnections: 3, maxConnections: 50, diskUsageGB: 2, databaseSizeBytes: 2048 });
  if (url.endsWith('/backup/list')) return ok({ backups: [{ id: 'b1', timestamp: '2026-10-02T00:00:00Z', status: 'COMPLETED' }, { id: 'b2', timestamp: '2026-10-03T00:00:00Z', status: 'FAILED' }] });
  if (url.endsWith('/apps/api')) return ok(APP);
  if (url.includes('/apps/api/deploys')) return ok([{ id: 'd1', appId: 'api', revision: 1, image: 'x', status: 'succeeded', createdBy: '1', createdAt: '2026-10-01T00:00:00Z' }]);
  if (url.endsWith('/sites/web')) return ok(SITE);
  if (url.endsWith('/sites/web/deploys')) return ok([{ id: 'd1', siteId: 'web', status: 'SUCCEEDED', fileCount: 1, totalBytes: 1, spaFallback: false, createdBy: '1', createdAt: '2026-10-01T00:00:00Z', live: true }]);
  if (url.endsWith('/storage/buckets')) return ok([{ id: 'b', name: 'avatars', public: false }]);
  return Promise.reject(Object.assign(new Error('not mocked'), { response: { status: 404, data: {} } }));
}

function renderPanel(service: ServiceRef, tab: string, opts: { database?: DatabaseInstance } = {}) {
  const onTab = vi.fn();
  const onClose = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <ServicePanel projectId="p1" service={service} tab={tab} database={opts.database ?? database()} onTab={onTab} onClose={onClose} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { onTab, onClose };
}

beforeEach(() => {
  vi.clearAllMocks();
  orgRole = 'owner';
  useAuthStore.setState({ user: { id: '1', username: 'u', email: 'e', role: 'user' } } as never);
  vi.mocked(api.get).mockImplementation(route as never);
  vi.mocked(api.post).mockResolvedValue({ data: { status: 'accepted' } } as never);
});

describe('database panel', () => {
  test('overview reads live state, the newest backup and offers pause', async () => {
    renderPanel({ kind: 'database' }, 'overview');
    expect(await screen.findByText('3 of 50')).toBeInTheDocument();
    expect(screen.getByText('2.0 KB')).toBeInTheDocument();
    expect(await screen.findByText(/Failed · /)).toBeInTheDocument();
    expect(screen.getByText(/paused automatically|paused after|no activity for 7 days/)).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Pause database' })).toBeEnabled();
  });

  test('pausing asks the server to pause this project', async () => {
    renderPanel({ kind: 'database' }, 'overview');
    await userEvent.click(await screen.findByRole('button', { name: 'Pause database' }));
    expect(api.post).not.toHaveBeenCalled();
    await userEvent.click(await screen.findByRole('button', { name: 'Yes, pause' }));
    expect(api.post).toHaveBeenCalledWith('/provision/p1/pause', { reason: 'manual' }, expect.anything());
  });

  test('pause can be cancelled without a request', async () => {
    renderPanel({ kind: 'database' }, 'overview');
    await userEvent.click(await screen.findByRole('button', { name: 'Pause database' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Cancel' }));
    expect(api.post).not.toHaveBeenCalled();
  });

  test('a viewer sees the pause control disabled and is told to ask an admin', async () => {
    orgRole = 'viewer';
    renderPanel({ kind: 'database' }, 'overview');
    expect(await screen.findByText(/Ask an admin/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Pause database' })).toBeDisabled();
    expect(screen.getByText(/Only an admin of this organization can delete this project/)).toBeInTheDocument();
  });

  test('a paused database offers resume instead, without the list of what goes down', async () => {
    renderPanel({ kind: 'database' }, 'overview', { database: database({ status: 'PAUSED' }) });
    await userEvent.click(await screen.findByRole('button', { name: 'Resume database' }));
    expect(api.post).toHaveBeenCalledWith('/provision/p1/resume', undefined, expect.anything());
    expect(screen.queryByText('What goes down')).not.toBeInTheDocument();
  });

  test('a database paused because the credit ran out says so and that it resumes by itself', async () => {
    renderPanel({ kind: 'database' }, 'overview', { database: database({ status: 'PAUSED', pauseReason: 'credits' }) });
    expect(await screen.findByText(/paused because your credit is used up/i)).toBeInTheDocument();
    expect(screen.getByText(/starts again by itself when credit is added/i)).toBeInTheDocument();
    expect(screen.queryByText(/upgrade/i)).not.toBeInTheDocument();
  });

  test('a database paused by the user says nothing about credit', async () => {
    renderPanel({ kind: 'database' }, 'overview', { database: database({ status: 'PAUSED', pauseReason: 'manual' }) });
    await screen.findByRole('button', { name: 'Resume database' });
    expect(screen.queryByText(/credit is used up/i)).not.toBeInTheDocument();
  });

  const creditRefusal = (need: { cpuMilli: number; memoryBytes: number }) => ({
    response: { status: 409, data: { code: 'plan_budget_exceeded', creditExhausted: true, dimension: 'CPU', need, budget: { cpuMilli: 1000, memoryBytes: 1024 * 1024 ** 2, diskBytes: 0, services: 5 }, error: 'your credit is used up, so this cannot start or grow: it needs 0.50 vCPU more CPU but only 0.00 vCPU is left of your free allowance. Contact support to add credit, or pause or delete other services' } },
  });

  test('resuming at $0 explains that a database that fits the free allowance starts once other services are paused', async () => {
    vi.mocked(api.post).mockRejectedValueOnce(creditRefusal({ cpuMilli: 500, memoryBytes: 512 * 1024 ** 2 }) as never);
    renderPanel({ kind: 'database' }, 'overview', { database: database({ status: 'PAUSED', pauseReason: 'idle_7d' }) });
    await userEvent.click(await screen.findByRole('button', { name: 'Resume database' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(/your credit is used up/i);
    expect(alert).toHaveTextContent(/fits your free allowance once other services are paused/i);
    expect(alert).toHaveTextContent(/contact support to add credit: hello@excalibase\.io/i);
    expect(within(alert).getByRole('link', { name: 'Usage & credits' })).toHaveAttribute('href', '/usage');
  });

  test('resuming at $0 a database bigger than the free allowance gets no claim that it would fit', async () => {
    vi.mocked(api.post).mockRejectedValueOnce(creditRefusal({ cpuMilli: 2000, memoryBytes: 512 * 1024 ** 2 }) as never);
    renderPanel({ kind: 'database' }, 'overview', { database: database({ status: 'PAUSED', pauseReason: 'idle_7d' }) });
    await userEvent.click(await screen.findByRole('button', { name: 'Resume database' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(/your credit is used up/i);
    expect(alert).not.toHaveTextContent(/fits your free allowance/i);
  });

  test('a plan budget refusal that is not about credit gets no credit hint even when the database is small', async () => {
    const refusal = creditRefusal({ cpuMilli: 500, memoryBytes: 512 * 1024 ** 2 });
    refusal.response.data.creditExhausted = false;
    vi.mocked(api.post).mockRejectedValueOnce(refusal as never);
    renderPanel({ kind: 'database' }, 'overview', { database: database({ status: 'PAUSED', pauseReason: 'idle_7d' }) });
    await userEvent.click(await screen.findByRole('button', { name: 'Resume database' }));
    const alert = await screen.findByRole('alert');
    expect(alert).not.toHaveTextContent(/fits your free allowance/i);
  });

  test('any other failed resume shows only the server sentence, with no credit hint', async () => {
    vi.mocked(api.post).mockRejectedValueOnce({ response: { status: 409, data: { error: 'another operation is running' } } } as never);
    renderPanel({ kind: 'database' }, 'overview', { database: database({ status: 'PAUSED' }) });
    await userEvent.click(await screen.findByRole('button', { name: 'Resume database' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('another operation is running');
    expect(alert).not.toHaveTextContent(/free allowance/i);
  });

  test('delete asks for the project name before it does anything', async () => {
    renderPanel({ kind: 'database' }, 'overview');
    await userEvent.click(await screen.findByTestId('delete-project-btn'));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('button', { name: 'Delete Project' })).toBeDisabled();
    expect(api.delete).not.toHaveBeenCalled();
  });

  test('every database tab opens something', async () => {
    const { onTab } = renderPanel({ kind: 'database' }, 'connect');
    expect(await screen.findByTestId('db-connect')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('tab', { name: 'Metrics' }));
    expect(onTab).toHaveBeenCalledWith('metrics');
  });

  test.each(['settings', 'data', 'backups', 'metrics', 'logs'])('the %s tab renders inside the panel', async (tab) => {
    renderPanel({ kind: 'database' }, tab);
    expect(await screen.findByRole('tab', { name: new RegExp(tab, 'i'), selected: true })).toBeInTheDocument();
    expect(screen.getByRole('tabpanel')).toBeInTheDocument();
  });

  test('the close button closes', async () => {
    const { onClose } = renderPanel({ kind: 'database' }, 'overview');
    await userEvent.click(screen.getByRole('button', { name: 'Close panel' }));
    expect(onClose).toHaveBeenCalled();
  });
});

describe('panel frame', () => {
  test('moves focus to the panel when it opens and closes on Escape', async () => {
    const { onClose } = renderPanel({ kind: 'database' }, 'overview');
    const panel = screen.getByRole('region', { name: 'Database panel' });
    expect(panel).toHaveFocus();
    await userEvent.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe('app panel', () => {
  test('a failed redeploy shows its error in the Deploys tab', async () => {
    vi.mocked(api.get).mockImplementation(((url: string) => (url.includes('/apps/api/deploys')
      ? Promise.resolve({ data: [
        { id: 'd2', appId: 'api', revision: 2, image: 'x', status: 'succeeded', createdBy: '1', createdAt: '2026-10-02T00:00:00Z' },
        { id: 'd1', appId: 'api', revision: 1, image: 'x', status: 'succeeded', createdBy: '1', createdAt: '2026-10-01T00:00:00Z' },
      ] })
      : route(url))) as never);
    vi.mocked(api.post).mockRejectedValue(Object.assign(new Error('x'), { response: { status: 409, data: { error: 'Rollout already running' } } }));
    renderPanel({ kind: 'app', id: 'api' }, 'deploys');
    await userEvent.click(await screen.findByTestId('redeploy-d1'));
    await userEvent.click(await screen.findByTestId('redeploy-confirm-d1'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/redeploy|rollout already running/i);
  });

  test.each(tabsFor('app').map((t) => t.key))('the %s tab renders', async (tab) => {
    renderPanel({ kind: 'app', id: 'api' }, tab);
    expect(await screen.findByRole('tab', { name: new RegExp(tab, 'i'), selected: true })).toBeInTheDocument();
    expect(await screen.findByRole('tabpanel')).toBeInTheDocument();
  });

  test('overview offers deploy and the app url', async () => {
    renderPanel({ kind: 'app', id: 'api' }, 'overview');
    expect(await screen.findByRole('button', { name: /Deploy/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'https://api.example.app' })).toBeInTheDocument();
  });

  test('variables are listed and link to the editor', async () => {
    renderPanel({ kind: 'app', id: 'api' }, 'variables');
    expect(await screen.findByTestId('env-view-LOG')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Edit variables/ })).toHaveAttribute('href', '/project/p1/containers/api/edit');
  });

  test('an app that cannot be loaded says so', async () => {
    vi.mocked(api.get).mockImplementation(((url: string) => (url.endsWith('/apps/gone') ? Promise.reject(new Error('nope')) : route(url))) as never);
    renderPanel({ kind: 'app', id: 'gone' }, 'overview');
    expect(await screen.findByRole('alert')).toBeInTheDocument();
  });
});

describe('site panel', () => {
  test.each(tabsFor('site').map((t) => t.key))('the %s tab renders', async (tab) => {
    renderPanel({ kind: 'site', id: 'web' }, tab);
    expect(await screen.findByRole('tab', { name: new RegExp(tab, 'i'), selected: true })).toBeInTheDocument();
    expect(await screen.findByRole('tabpanel')).toBeInTheDocument();
  });

  test('a static site says it has no variables', async () => {
    renderPanel({ kind: 'site', id: 'web' }, 'variables');
    expect(await screen.findByText(/no variables/i)).toBeInTheDocument();
  });

  test('a viewer cannot deploy or delete', async () => {
    orgRole = 'viewer';
    renderPanel({ kind: 'site', id: 'web' }, 'settings');
    expect(await screen.findByText('Custom domains')).toBeInTheDocument();
    expect(screen.queryByTestId('site-delete')).not.toBeInTheDocument();
  });

  test('a site that cannot be loaded says so', async () => {
    vi.mocked(api.get).mockImplementation(((url: string) => (url.endsWith('/sites/gone') ? Promise.reject(new Error('nope')) : route(url))) as never);
    renderPanel({ kind: 'site', id: 'gone' }, 'overview');
    expect(await screen.findByRole('alert')).toBeInTheDocument();
  });
});

describe('storage panel', () => {
  test('a bucket opens the storage view with no tabs', async () => {
    renderPanel({ kind: 'storage', id: 'avatars' }, '');
    expect(await screen.findByRole('heading', { name: 'avatars' })).toBeInTheDocument();
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
  });

  test('a database panel is not built for a project that has no database', () => {
    const client = new QueryClient();
    const { container } = render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <ServicePanel projectId="p1" service={{ kind: 'database' }} tab="data" onTab={vi.fn()} onClose={vi.fn()} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
