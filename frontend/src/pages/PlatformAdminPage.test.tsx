import { describe, test, expect, beforeEach, afterEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
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
vi.mock('../components/SignInProviders', () => ({ SignInProviders: () => null }));

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
const orgs = [{ id: 'o1', slug: 'acme', name: 'Acme' }];

const refusal = (reason: string) => ({
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: reason } },
});

function renderPage(role: string, answers: { capacity?: unknown; projects?: unknown[] } = {}) {
  useAuthStore.getState().setAuth({ id: 'u1', username: 'op', email: 'op@example.com', role } as never);
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/capacity') return Promise.resolve({ data: answers.capacity ?? capacity } as never);
    if (url === '/admin/projects') return Promise.resolve({ data: answers.projects ?? projects } as never);
    if (url === '/orgs') return Promise.resolve({ data: orgs } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/admin']}>
        <Routes>
          <Route path="/admin" element={<PlatformAdminPage />} />
          <Route path="/" element={<div data-testid="home" />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('PlatformAdminPage', () => {
  let alert: ReturnType<typeof vi.spyOn>;
  let confirm: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    vi.clearAllMocks();
    alert = vi.spyOn(globalThis, 'alert').mockImplementation(() => {});
    confirm = vi.spyOn(globalThis, 'confirm').mockReturnValue(true);
  });

  afterEach(() => {
    alert.mockRestore();
    confirm.mockRestore();
    useAuthStore.getState().clearAuth();
  });

  test('anyone but a platform admin or operator is sent home', () => {
    renderPage('user');
    expect(screen.getByTestId('home')).toBeInTheDocument();
  });

  test('shows cluster capacity, every project and every org', async () => {
    renderPage('platform_admin');
    expect(await screen.findByText('3.60 / 4.00 cores')).toBeInTheDocument();
    expect(screen.getByText('5.0 / 8.0 GiB')).toBeInTheDocument();
    expect(screen.getByText(/limited by memory/)).toBeInTheDocument();
    expect(await screen.findByText('shop')).toBeInTheDocument();
    expect(screen.getByText('256 MiB')).toBeInTheDocument();
    expect(screen.getAllByText('—')).toHaveLength(2);
    expect(await screen.findByText('acme')).toBeInTheDocument();
  });

  test('an empty cluster reads as zero use with no projects', async () => {
    renderPage('platform_admin', {
      capacity: { ...capacity, usableCpuMilli: 0, usableMemBytes: 0, requestedCpuMilli: 0, requestedMemBytes: 0 },
      projects: [],
    });
    expect(await screen.findByText('No projects provisioned.')).toBeInTheDocument();
    expect(await screen.findByText('0.00 / 0.00 cores')).toBeInTheDocument();
  });

  test("a refused drop alerts the server's reason", async () => {
    vi.mocked(api.delete).mockRejectedValueOnce(refusal('the project is still restoring'));
    renderPage('platform_admin');
    await userEvent.click((await screen.findAllByRole('button', { name: /Drop/ }))[0]);

    await waitFor(() => expect(alert).toHaveBeenCalledWith('Drop failed: the project is still restoring'));
    expect(api.delete).toHaveBeenCalledWith('/admin/projects/p1');
  });

  test("a refused revoke alerts the server's reason", async () => {
    vi.mocked(api.delete).mockRejectedValueOnce(refusal('the org owns the platform'));
    renderPage('platform_admin');
    await screen.findByText('acme');
    await userEvent.click(screen.getByRole('button', { name: /Revoke/ }));

    await waitFor(() => expect(alert).toHaveBeenCalledWith('Revoke failed: the org owns the platform'));
    expect(api.delete).toHaveBeenCalledWith('/admin/orgs/o1?cascade=true');
    expect(alert.mock.calls[0][0]).not.toMatch(/status code/);
  });

  test('a drop or revoke that is not confirmed sends nothing', async () => {
    confirm.mockReturnValue(false);
    renderPage('platform_admin');
    await userEvent.click((await screen.findAllByRole('button', { name: /Drop/ }))[0]);
    await screen.findByText('acme');
    await userEvent.click(screen.getByRole('button', { name: /Revoke/ }));
    expect(api.delete).not.toHaveBeenCalled();
  });

  test('an operator sees the tables but cannot drop or revoke', async () => {
    renderPage('platform_operator');
    await screen.findByText('acme');
    for (const button of screen.getAllByRole('button', { name: /Drop|Revoke/ })) {
      expect(button).toBeDisabled();
    }
  });
});
