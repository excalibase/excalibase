import { describe, test, expect, beforeEach, afterEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { PlatformAdminPage } from './PlatformAdminPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), delete: vi.fn() } }));
// These cards have their own tests; here they would only add requests to answer.
vi.mock('../components/TierConfigTable', () => ({ TierConfigTable: () => null }));
vi.mock('../components/StorageBudgetCard', () => ({ StorageBudgetCard: () => null }));
vi.mock('../components/SignInProviders', () => ({ SignInProviders: () => <div data-testid="sign-in-providers" /> }));
vi.mock('../components/admin/NeedsAttention', () => ({ NeedsAttention: () => <div data-testid="needs-attention" /> }));
vi.mock('../components/admin/v2/UsersAdminTable', () => ({ UsersAdminTable: () => <div data-testid="users-table" /> }));
vi.mock('../components/admin/v2/OrgsAdminTable', () => ({ OrgsAdminTable: () => <div data-testid="orgs-table" /> }));
vi.mock('../components/admin/ProjectDetailPanel', () => ({
  ProjectDetailPanel: ({ projectId }: { projectId: string }) => <div data-testid="project-panel">{projectId}</div>,
}));

const capacity = {
  usableCpuMilli: 4000,
  requestedCpuMilli: 3600,
  usableMemBytes: 8 * 1024 ** 3,
  requestedMemBytes: 5 * 1024 ** 3,
  headroomPercent: 20,
  projects: { total: 2, byTier: {} },
  tiers: {
    free: { projectsCanFit: 4, limitedBy: 'cpu', currentlyProvisioned: 1 },
    standard: { projectsCanFit: 1, limitedBy: 'memory', currentlyProvisioned: 1 },
  },
};
const projects = [
  { projectId: 'p1', projectName: 'shop', orgId: 'o1', status: 'RUNNING', tier: 'free', cpuCores: 0.25, memBytes: 256 * 1024 * 1024 },
  { projectId: 'p2', projectName: 'blog', orgId: 'o1', status: 'RUNNING', tier: 'standard' },
];

const refusal = (reason: string) => ({
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: reason } },
});

interface Answers {
  capacity?: unknown;
  projects?: unknown[];
  features?: Record<string, boolean>;
}

function renderPage(role: string, answers: Answers = {}, path = '/admin') {
  useAuthStore.getState().setAuth({ id: 'u1', username: 'op', email: 'op@example.com', role } as never);
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features: answers.features ?? {} } } as never);
    if (url === '/capacity') return Promise.resolve({ data: answers.capacity ?? capacity } as never);
    if (url === '/admin/projects') {
      const items = answers.projects ?? projects;
      return Promise.resolve({ data: { items, total: items.length } } as never);
    }
    if (url === '/admin/analytics/users') return Promise.resolve({ data: { windowDays: 30, signups: 292, suspended: 96, activeLast7Days: 5, perDay: [], funnel: [], domains: [] } } as never);
    if (url === '/admin/analytics/abuse') return Promise.resolve({ data: { windowDays: 30, perDay: [], proxyRefusals: 0, registryRefusals: 0, refusedUsers: 0, refusedStillActive: 0, suspended: 0, latest: [] } } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/admin" element={<PlatformAdminPage />} />
          <Route path="/" element={<div data-testid="home" />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const analyticsCalls = () => vi.mocked(api.get).mock.calls.filter(([url]) => String(url).startsWith('/admin/analytics'));

describe('PlatformAdminPage', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => useAuthStore.getState().clearAuth());

  test('anyone but a platform admin or operator is sent home', () => {
    renderPage('user');
    expect(screen.getByTestId('home')).toBeInTheDocument();
  });

  test.each([
    ['off', { studio_v2: false }],
    ['on', { studio_v2: true }],
    ['absent', {}],
  ])('the console is the tabbed layout whether studio_v2 is %s', async (_name, features) => {
    renderPage('platform_admin', { features });
    expect(await screen.findByTestId('admin-console-v2')).toBeInTheDocument();
    expect(await screen.findByTestId('needs-attention')).toBeInTheDocument();
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Needs attention', 'Projects', 'Users', 'Organizations', 'Platform']);
  });

  test('the platform tab keeps cluster capacity, plans, storage and sign-in providers', async () => {
    renderPage('platform_admin');
    await userEvent.click(await screen.findByRole('tab', { name: 'Platform' }));
    expect(await screen.findByText('3.60 / 4.00 cores')).toBeInTheDocument();
    expect(screen.getByText('5.0 / 8.0 GiB')).toBeInTheDocument();
    expect(screen.getByText(/limited by memory/)).toBeInTheDocument();
    expect(screen.getByTestId('sign-in-providers')).toBeInTheDocument();
  });

  test('an empty cluster reads as zero use', async () => {
    renderPage('platform_admin', {
      capacity: { ...capacity, usableCpuMilli: 0, usableMemBytes: 0, requestedCpuMilli: 0, requestedMemBytes: 0 },
    });
    await userEvent.click(await screen.findByRole('tab', { name: 'Platform' }));
    expect(await screen.findByText('0.00 / 0.00 cores')).toBeInTheDocument();
  });

  test('the projects tab lists every project with its usage', async () => {
    renderPage('platform_admin');
    await userEvent.click(await screen.findByRole('tab', { name: 'Projects' }));
    expect(await screen.findByText('shop')).toBeInTheDocument();
    expect(screen.getByText('blog')).toBeInTheDocument();
  });

  test('a drop needs a written reason and sends it', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({ status: 200, data: { status: 'deprovisioned' } } as never);
    renderPage('platform_admin');
    await userEvent.click(await screen.findByRole('tab', { name: 'Projects' }));
    await userEvent.click((await screen.findAllByRole('button', { name: /Drop/ }))[0]);
    const dialog = screen.getByRole('dialog');
    const confirmButton = within(dialog).getByRole('button', { name: 'Drop' });
    expect(confirmButton).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'abandoned and holding a node');
    await userEvent.click(confirmButton);
    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/admin/projects/p1', { headers: { Prefer: 'respond-async' }, data: { reason: 'abandoned and holding a node' } }),
    );
  });

  test("a refused drop shows the server's reason in the dialog", async () => {
    vi.mocked(api.delete).mockRejectedValueOnce(refusal('the project is still restoring'));
    renderPage('platform_admin');
    await userEvent.click(await screen.findByRole('tab', { name: 'Projects' }));
    await userEvent.click((await screen.findAllByRole('button', { name: /Drop/ }))[0]);
    const dialog = screen.getByRole('dialog');
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'abandoned and holding a node');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Drop' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('the project is still restoring');
  });

  test('a drop that is cancelled sends nothing', async () => {
    renderPage('platform_admin');
    await userEvent.click(await screen.findByRole('tab', { name: 'Projects' }));
    await userEvent.click((await screen.findAllByRole('button', { name: /Drop/ }))[0]);
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(api.delete).not.toHaveBeenCalled();
  });

  test('an operator sees projects and the platform cards, and cannot drop', async () => {
    renderPage('platform_operator');
    expect(await screen.findByText('shop')).toBeInTheDocument();
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Projects', 'Platform']);
    expect(screen.queryByTestId('needs-attention')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Open/ })).not.toBeInTheDocument();
    for (const button of screen.getAllByRole('button', { name: /Drop/ })) {
      expect(button).toBeDisabled();
    }
  });

  test('a project of a suspended owner reads Suspended and names its owner', async () => {
    renderPage('platform_admin', {
      projects: [{ projectId: 'p1', projectName: 'shop', orgId: 'o1', orgName: 'Acme', ownerEmail: 'bad@mailinator.test', ownerStatus: 'suspended', ownerSuspended: true, status: 'ACTIVE', tier: 'free' }],
    });
    await userEvent.click(await screen.findByRole('tab', { name: 'Projects' }));
    const row = (await screen.findByText('shop')).closest('tr') as HTMLElement;
    expect(within(row).getByText('Suspended')).toBeInTheDocument();
    expect(within(row).getByText('bad@mailinator.test')).toBeInTheDocument();
  });

  test('Open puts the project in the address and shows its panel', async () => {
    renderPage('platform_admin');
    await userEvent.click(await screen.findByRole('tab', { name: 'Projects' }));
    await userEvent.click((await screen.findAllByRole('button', { name: /Open/ }))[0]);
    expect(await screen.findByTestId('project-panel')).toHaveTextContent('p1');
  });

  test('a link from needs-attention opens the project it names', async () => {
    renderPage('platform_admin', {}, '/admin?project=p2');
    expect(await screen.findByTestId('project-panel')).toHaveTextContent('p2');
  });
});

describe('PlatformAdminPage analytics tabs (EXC-898)', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => useAuthStore.getState().clearAuth());

  test('with the flag off there are no analytics tabs and the server is not asked', async () => {
    renderPage('platform_admin', { features: { adminanalytics: false } });
    await screen.findByTestId('needs-attention');
    await waitFor(() => expect(vi.mocked(api.get).mock.calls.some(([url]) => url === '/config')).toBe(true));
    expect(screen.queryByRole('tab', { name: /Abuse|Traffic|Storage|Sign-ups/ })).not.toBeInTheDocument();
    expect(analyticsCalls()).toHaveLength(0);
  });

  test('with the flag on a platform admin gets the analytics tabs in the same bar, with counts', async () => {
    renderPage('platform_admin', { features: { adminanalytics: true } });
    const signups = await screen.findByRole('tab', { name: /Sign-ups/ });
    await waitFor(() => expect(signups).toHaveTextContent('292'));
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent?.replace(/\d+|no data|not collected/g, '').trim())).toEqual(
      ['Needs attention', 'Projects', 'Users', 'Organizations', 'Sign-ups', 'Abuse', 'Traffic', 'Storage', 'Platform'],
    );
  });

  test('an operator never gets the analytics tabs, flag or not', async () => {
    renderPage('platform_operator', { features: { adminanalytics: true } });
    await screen.findByRole('tab', { name: 'Platform' });
    await waitFor(() => expect(vi.mocked(api.get).mock.calls.some(([url]) => url === '/config')).toBe(true));
    expect(screen.queryByRole('tab', { name: /Abuse/ })).not.toBeInTheDocument();
    expect(analyticsCalls()).toHaveLength(0);
  });
});
