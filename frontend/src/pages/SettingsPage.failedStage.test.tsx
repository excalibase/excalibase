import { describe, test, expect, beforeEach, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SettingsPage } from './SettingsPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

const settle = () => act(async () => new Promise((resolve) => setTimeout(resolve, 150)));

// EXC-850: the failed-provisioning banner names the step in product words.
describe('SettingsPage — failed provisioning banner', () => {
  function serve(stages: Record<string, unknown>) {
    vi.mocked(api.get).mockImplementation((url: string) =>
      url === '/provision/p-1'
        ? Promise.resolve({
            data: {
              projectId: 'p-1', orgId: 'o-1', databaseType: 'POSTGRESQL', tier: 'FREE', status: 'FAILED',
              deletionProtection: false, ...stages,
            },
          } as never)
        : Promise.reject(new Error(`unexpected GET ${url}`)),
    );
  }

  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.setState({ user: { id: 'u1', username: 'u', email: 'u@e.test', role: 'user' }, isAuthenticated: true });
  });

  function renderSettings() {
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter initialEntries={['/project/p-1/settings']}>
          <Routes>
            <Route path="/project/:projectId/settings" element={<SettingsPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
  }

  test('shows the friendly stage label, never the raw stage or step', async () => {
    serve({ currentStage: 'FAILED', failureStage: 'NAMESPACE_CREATION', failureStep: 'create namespace' });
    renderSettings();
    const banner = await screen.findByText(/Provisioning failed at/);
    expect(banner).toHaveTextContent('Provisioning failed at stage Environment');
    expect(banner.textContent).not.toMatch(/NAMESPACE|namespace/i);
  });

  test('falls back to the current stage when no failure stage is recorded', async () => {
    serve({ currentStage: 'CRD_DEPLOYMENT' });
    renderSettings();
    expect(await screen.findByText(/Provisioning failed at/)).toHaveTextContent(
      'Provisioning failed at stage Database',
    );
  });

  test.each([
    ['an unknown stage', { currentStage: 'FAILED', failureStage: 'SOMETHING_NEW' }],
    ['no stage at all', { currentStage: 'FAILED' }],
  ])('says only "Provisioning failed" for %s, never a blank or a raw key', async (_name, stages) => {
    serve(stages);
    renderSettings();
    const headline = await screen.findByText(/^Provisioning failed$/);
    expect(headline.textContent).not.toMatch(/SOMETHING_NEW|FAILED|unknown/);
  });

  const KUBE_TEXT = 'create db: pod db-1 CrashLoopBackOff on node n7';
  const ROLLBACK = JSON.stringify([{ name: 'delete namespace p-1', ok: true }]);

  test('a customer sees plain wording and neither the raw text nor the rollback log', async () => {
    serve({ currentStage: 'FAILED', failureStage: 'CRD_DEPLOYMENT', failureReason: KUBE_TEXT, rollbackLog: ROLLBACK });
    renderSettings();
    expect(await screen.findByText(/kept crashing/i)).toBeInTheDocument();
    await settle();
    expect(screen.queryByText(/CrashLoopBackOff/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Rollback log/)).not.toBeInTheDocument();
    expect(screen.queryByText('Technical details')).not.toBeInTheDocument();
  });

  test('an unknown failure text gives the generic wording and is never shown', async () => {
    serve({ currentStage: 'FAILED', failureStage: 'CRD_DEPLOYMENT', failureReason: 'weird internal text' });
    renderSettings();
    expect(await screen.findByText('Provisioning failed. Delete it and add it again. If it keeps failing, tell us.')).toBeInTheDocument();
    await settle();
    expect(screen.queryByText(/weird internal text/)).not.toBeInTheDocument();
  });

  test('a platform admin finds the raw text and rollback log under collapsed Technical details', async () => {
    useAuthStore.setState({ user: { id: 'a1', username: 'a', email: 'a@e.test', role: 'platform_admin' }, isAuthenticated: true });
    serve({ currentStage: 'FAILED', failureStage: 'CRD_DEPLOYMENT', failureReason: KUBE_TEXT, rollbackLog: ROLLBACK });
    renderSettings();
    const block = (await screen.findByText('Technical details')).closest('details');
    expect(block).not.toHaveAttribute('open');
    expect(block).toHaveTextContent(KUBE_TEXT);
    expect(block).toHaveTextContent('delete namespace p-1');
  });
});
