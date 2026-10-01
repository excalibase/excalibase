import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { InstancesPage } from './InstancesPage';
import { InstanceDetailPage } from './InstanceDetailPage';
import { DatabaseInstanceCard } from '../components/DatabaseInstanceCard';
import { useAuthStore } from '../stores/auth-store';
import { api } from '../api/client';
import type { DatabaseInstance } from '../types';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const REASON = /deletion protection is on/i;

function instance(projectId: string, deletionProtection: boolean): DatabaseInstance {
  return {
    id: 1, projectId, orgId: 'o-1', databaseType: 'POSTGRESQL', tier: 'FREE', namespace: `o-1-${projectId}`,
    host: 'h', port: 5432, databaseName: 'app', username: 'app', status: 'ACTIVE', currentStage: 'COMPLETED',
    backupEnabled: false, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z', deletionProtection,
  } as DatabaseInstance;
}

function withProviders(ui: React.ReactNode, path = '/', entry = '/') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path={path} element={ui} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision') return Promise.resolve({ data: [instance('p-on', true), instance('p-off', false)] } as never);
    if (url === '/provision/p-on') return Promise.resolve({ data: instance('p-on', true) } as never);
    if (url === '/provision/p-off') return Promise.resolve({ data: instance('p-off', false) } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
});

describe('older Studio pages honour deletion protection', () => {
  test('instances list disables Delete on a protected project and says why', async () => {
    useAuthStore.setState({ user: { id: 'u', username: 'a', role: 'platform_admin' } as never, isAuthenticated: true });
    withProviders(<InstancesPage />);

    const protectedBtn = await screen.findByTestId('delete-instance-p-on');
    expect(protectedBtn).toBeDisabled();
    expect(protectedBtn).toHaveAttribute('title', expect.stringMatching(REASON));
    expect(screen.getByTestId('delete-instance-p-off')).toBeEnabled();
  });

  test('instance detail disables Deprovision on a protected project and says why', async () => {
    withProviders(<InstanceDetailPage />, '/project/:projectId', '/project/p-on');
    expect(await screen.findByTestId('deprovision-btn')).toBeDisabled();
    expect(screen.getByText(REASON)).toBeInTheDocument();
  });

  test('instance detail allows Deprovision on an unprotected project', async () => {
    withProviders(<InstanceDetailPage />, '/project/:projectId', '/project/p-off');
    expect(await screen.findByTestId('deprovision-btn')).toBeEnabled();
    expect(screen.queryByText(REASON)).toBeNull();
  });

  test('instance card disables Delete on a protected project and says why', async () => {
    withProviders(<DatabaseInstanceCard instance={instance('p-on', true)} />);
    await userEvent.click(screen.getByTestId('instance-card-expand'));
    const btn = screen.getByTestId('instance-card-delete');
    expect(btn).toBeDisabled();
    expect(btn).toHaveAttribute('title', expect.stringMatching(REASON));
  });

  test('instance card allows Delete on an unprotected project', async () => {
    withProviders(<DatabaseInstanceCard instance={instance('p-off', false)} />);
    await userEvent.click(screen.getByTestId('instance-card-expand'));
    expect(screen.getByTestId('instance-card-delete')).toBeEnabled();
  });

  test('an unprotected project is still deleted from the list', async () => {
    vi.spyOn(globalThis, 'confirm').mockReturnValue(true);
    vi.mocked(api.delete).mockResolvedValue({} as never);
    useAuthStore.setState({ user: { id: 'u', username: 'a', role: 'platform_admin' } as never, isAuthenticated: true });
    withProviders(<InstancesPage />);
    await userEvent.click(await screen.findByTestId('delete-instance-p-off'));
    expect(api.delete).toHaveBeenCalledWith('/provision/p-off', { headers: { Prefer: 'respond-async' } });
  });
});
