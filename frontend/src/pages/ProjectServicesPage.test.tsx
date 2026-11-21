import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { ProjectServicesPage } from './ProjectServicesPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }));

const project = (extra: Record<string, unknown> = {}) => ({
  projectId: 'p1', projectName: 'Shop', orgId: 'org-1', databaseType: 'POSTGRESQL', postgresVersion: '17', tier: 'FREE',
  status: 'ACTIVE', currentStage: 'COMPLETED', ...extra,
});
const app = { id: 'api', projectId: 'p1', name: 'api', image: 'ghcr.io/me/api:1', env: [], port: 8080, replicas: 1, tier: 'FREE', status: 'ACTIVE', version: 1 };
const deploy = (status: string, revision = 1) => ({ id: `d${revision}`, appId: 'api', projectId: 'p1', revision, image: 'x', status, createdBy: '1', createdAt: '2026-10-01T00:00:00Z' });

interface World {
  project?: unknown;
  apps?: unknown[];
  deploys?: unknown[];
  buckets?: unknown[];
  functions?: unknown[];
  sites?: unknown[];
  projectError?: boolean;
  bucketsError?: boolean;
  config?: Record<string, unknown>;
}

function stub(world: World) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    const data = (body: unknown) => Promise.resolve({ data: body });
    if (url === '/config') return data({ deploymentMode: 'cloud', appHosting: true, sites: true, features: { studio_v2: true }, ...world.config });
    if (url === '/provision/p1') return world.projectError ? Promise.reject(new Error('boom')) : data(world.project ?? project());
    if (url === '/tiers') return data([{ tier: 'FREE', cpu: '1', memory: '2Gi' }]);
    if (url.endsWith('/apps/')) return data(world.apps ?? []);
    if (url.endsWith('/apps/api')) return data(app);
    if (url.includes('/apps/api/deploys')) return data(world.deploys ?? []);
    if (url.endsWith('/sites/')) return data(world.sites ?? []);
    if (url.endsWith('/storage/buckets')) return world.bucketsError ? Promise.reject(new Error('buckets down')) : data(world.buckets ?? []);
    if (url.endsWith('/functions')) return data(world.functions ?? []);
    return Promise.reject(Object.assign(new Error('not mocked'), { response: { status: 404 } }));
  });
}

function Where() {
  const { search } = useLocation();
  return <div data-testid="where">{search}</div>;
}

function renderAt(entry: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/project/:projectId" element={<><ProjectServicesPage /><Where /></>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('ProjectServicesPage', () => {
  test('lists a card per service with its own status', async () => {
    stub({ apps: [app], deploys: [deploy('succeeded')], buckets: [{ id: 'b', name: 'avatars', public: true }] });
    renderAt('/project/p1');
    const list = await screen.findByRole('list', { name: 'Services' });
    expect(await within(list).findByText('Postgres 17 · 1 vCPU')).toBeInTheDocument();
    expect(within(list).getByText('Running', { selector: 'span' }).closest('a')).toHaveTextContent('Database');
    expect(within(list).getByText('App · ghcr.io/me/api:1')).toBeInTheDocument();
    expect(within(list).getByText('Storage bucket · public')).toBeInTheDocument();
  });

  test('selecting a card writes it to the URL and opens its panel', async () => {
    stub({ apps: [app], deploys: [deploy('succeeded')] });
    renderAt('/project/p1');
    await userEvent.click(await screen.findByRole('link', { name: /api/ }));
    expect(screen.getByTestId('where')).toHaveTextContent('?service=app%3Aapi');
    const tabs = await screen.findByRole('tablist', { name: 'App' });
    expect(within(tabs).getAllByRole('tab').map((t) => t.textContent)).toEqual(['Overview', 'Deploys', 'Variables', 'Metrics', 'Logs', 'Settings']);
    await userEvent.click(within(tabs).getByRole('tab', { name: 'Variables' }));
    expect(screen.getByTestId('where')).toHaveTextContent('tab=variables');
    await userEvent.click(screen.getByRole('button', { name: 'Close panel' }));
    expect(screen.getByTestId('where')).toHaveTextContent('');
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
  });

  test('a deep link opens the database on the named tab', async () => {
    stub({});
    renderAt('/project/p1?service=database&tab=settings');
    expect(await screen.findByRole('tab', { name: 'Settings', selected: true })).toBeInTheDocument();
  });

  test('an empty project says to add a first service and offers the create flows', async () => {
    stub({ project: project({ noDatabase: true, canAddDatabase: true }) });
    renderAt('/project/p1');
    expect(await screen.findByRole('heading', { name: 'Add your first service' })).toBeInTheDocument();
    const empty = screen.getByTestId('services-empty');
    expect(within(empty).getByRole('link', { name: /Database/ })).toHaveAttribute('href', '/project/p1/database/add');
    expect(within(empty).getByRole('link', { name: /App/ })).toHaveAttribute('href', '/project/p1/containers/new');
    expect(within(empty).getByRole('link', { name: /Site/ })).toHaveAttribute('href', '/project/p1/sites/new');
  });

  test('the add menu does not offer a database to a caller who cannot add one', async () => {
    stub({ project: project({ noDatabase: true, canAddDatabase: false }), apps: [app], deploys: [deploy('succeeded')] });
    renderAt('/project/p1');
    await userEvent.click(await screen.findByRole('button', { name: '+ Add service' }));
    const menu = screen.getByRole('menu', { name: 'Add service' });
    expect(within(menu).queryByRole('menuitem', { name: /Database/ })).not.toBeInTheDocument();
    expect(within(menu).getByRole('menuitem', { name: /App/ })).toBeInTheDocument();
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  test('a project without a database never offers database pages, even from a link', async () => {
    stub({ project: project({ noDatabase: true }), apps: [app], deploys: [deploy('succeeded')] });
    renderAt('/project/p1?service=database&tab=data');
    expect(await screen.findByText(/not in this project/)).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'Data' })).not.toBeInTheDocument();
    expect(screen.queryByText('Database')).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: /api/ })).toBeInTheDocument();
  });

  test('a project that cannot be read is an error, not an empty project', async () => {
    stub({ projectError: true });
    renderAt('/project/p1');
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not load the project/i);
    expect(screen.queryByTestId('services-empty')).not.toBeInTheDocument();
  });
  test('functions are hidden like the nav hides them: no card, no menu entry, no request', async () => {
    stub({ apps: [app], deploys: [deploy('succeeded')], functions: [{ id: 'f' }, { id: 'g' }] });
    renderAt('/project/p1?service=functions');
    await userEvent.click(await screen.findByRole('button', { name: '+ Add service' }));
    expect(within(screen.getByRole('menu')).queryByRole('menuitem', { name: /Function/ })).not.toBeInTheDocument();
    expect(screen.queryByText(/2 functions/)).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Functions' })).not.toBeInTheDocument();
    expect(screen.getByText(/not in this project/)).toBeInTheDocument();
    expect(vi.mocked(api.get).mock.calls.some(([url]) => String(url).endsWith('/functions'))).toBe(false);
  });

  test('switching from one bucket card to another shows the second bucket', async () => {
    stub({ buckets: [{ id: 'b1', name: 'avatars', public: false }, { id: 'b2', name: 'photos', public: false }] });
    renderAt('/project/p1?service=storage%3Aavatars');
    expect(await screen.findByRole('region', { name: 'avatars panel' })).toBeInTheDocument();
    await userEvent.click(screen.getByRole('link', { name: /photos/ }));
    expect(await screen.findByRole('region', { name: 'photos panel' })).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'avatars panel' })).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'avatars' })).not.toBeInTheDocument();
  });

  test('a service list that failed to load is an error, never "Add your first service"', async () => {
    stub({ project: project({ noDatabase: true }), bucketsError: true });
    renderAt('/project/p1');
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not be loaded|buckets down/i);
    expect(screen.queryByRole('heading', { name: 'Add your first service' })).not.toBeInTheDocument();
  });

  test('a MySQL project has no Postgres-only cluster settings in its database panel', async () => {
    stub({ project: project({ databaseType: 'MYSQL' }) });
    renderAt('/project/p1?service=database&tab=settings');
    expect(await screen.findByTestId('db-settings')).toBeInTheDocument();
    expect(screen.queryByTestId('cluster-settings-card')).not.toBeInTheDocument();
  });

  test('a DocumentDB project names its engine and keeps the same panel', async () => {
    stub({ project: project({ documentDb: true }) });
    renderAt('/project/p1?service=database');
    expect(await screen.findByText(/DocumentDB/)).toBeInTheDocument();
    expect(await screen.findByTestId('panel-status')).toBeInTheDocument();
    expect(await screen.findByRole('tablist', { name: 'Database' })).toBeInTheDocument();
  });

  test('without app hosting or sites the page never asks for them or offers them', async () => {
    stub({ config: { appHosting: false, sites: false } });
    renderAt('/project/p1');
    await userEvent.click(await screen.findByRole('button', { name: '+ Add service' }));
    const menu = screen.getByRole('menu', { name: 'Add service' });
    expect(within(menu).queryByRole('menuitem', { name: /^App/ })).not.toBeInTheDocument();
    expect(within(menu).queryByRole('menuitem', { name: /^Site/ })).not.toBeInTheDocument();
    expect(within(menu).getByRole('menuitem', { name: /Storage/ })).toBeInTheDocument();
    const urls = vi.mocked(api.get).mock.calls.map(([url]) => String(url));
    expect(urls.some((url) => url.includes('/apps/'))).toBe(false);
    expect(urls.some((url) => url.includes('/sites/'))).toBe(false);
  });
});
