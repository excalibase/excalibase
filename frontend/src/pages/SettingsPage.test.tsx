import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SettingsPage } from './SettingsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

function renderSettings(
  deletionProtection: boolean,
  extra: Record<string, unknown> = {},
  otherGets: Record<string, unknown> = {},
) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url in otherGets) return Promise.resolve({ data: otherGets[url] } as never);
    if (url === '/provision/p-1') {
      return Promise.resolve({
        data: { projectId: 'p-1', orgId: 'o-1', databaseType: 'POSTGRESQL', tier: 'FREE', status: 'ACTIVE', deletionProtection, ...extra },
      } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  vi.mocked(api.post).mockResolvedValue({ data: { status: 'PAUSED' } } as never);
  vi.mocked(api.patch).mockResolvedValue({ data: { deletionProtection: !deletionProtection } } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p-1/settings']}>
        <Routes>
          <Route path="/project/:projectId/settings" element={<SettingsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

describe('SettingsPage — deletion protection', () => {
  beforeEach(() => vi.clearAllMocks());

  test('a protected project cannot be deleted until protection is turned off', async () => {
    renderSettings(true);
    expect(await screen.findByTestId('delete-project-btn')).toBeDisabled();

    await userEvent.click(screen.getByTestId('deletion-protection-btn'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/provision/p-1/deletion-protection', { enabled: false })
    );
  });

  test('an unprotected project can be deleted and protection turned back on', async () => {
    renderSettings(false);
    expect(await screen.findByTestId('delete-project-btn')).toBeEnabled();

    await userEvent.click(screen.getByTestId('deletion-protection-btn'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/provision/p-1/deletion-protection', { enabled: true })
    );
  });

  test('deleting says the project is kept for 7 days first', async () => {
    renderSettings(false);
    expect(await screen.findByTestId('deletion-grace-note')).toHaveTextContent(/7 days/);
  });

  test('a project scheduled for deletion shows when and can be cancelled', async () => {
    renderSettings(false, { status: 'PENDING_DELETION', deletionDueAt: '2026-10-05T10:00:00Z' });
    expect(await screen.findByTestId('deletion-scheduled')).toHaveTextContent(/scheduled for deletion/i);
    expect(screen.queryByTestId('delete-project-btn')).toBeNull();

    await userEvent.click(screen.getByTestId('cancel-deletion-btn'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/provision/p-1/deletion/cancel'));
  });
});

describe('SettingsPage — engine', () => {
  beforeEach(() => vi.clearAllMocks());

  test('names a DocumentDB project as DocumentDB (MongoDB-compatible)', async () => {
    renderSettings(false, { documentDb: true });
    expect(await screen.findByText('DocumentDB (MongoDB-compatible)')).toBeInTheDocument();
  });

  test('names a plain project PostgreSQL', async () => {
    renderSettings(false);
    expect(await screen.findByText('PostgreSQL')).toBeInTheDocument();
  });
});

describe('SettingsPage — public database port', () => {
  beforeEach(() => vi.clearAllMocks());

  test('offers the public database port control next to the connection strings', async () => {
    renderSettings(false);
    expect(await screen.findByTestId('public-port-card')).toBeInTheDocument();
  });
});

describe('SettingsPage — private network between apps', () => {
  beforeEach(() => vi.clearAllMocks());

  const gets = (appHosting: boolean) => ({
    '/config': { deploymentMode: 'cloud', appHosting },
    '/projects/p-1/app-network': { projectId: 'p-1', privateNetwork: false, applied: false, canChange: true },
  });

  test('offers the setting when the installation hosts apps', async () => {
    renderSettings(false, {}, gets(true));
    expect(await screen.findByTestId('app-network-card')).toBeInTheDocument();
  });

  test('says nothing about app networking when apps are not hosted', async () => {
    renderSettings(false, {}, gets(false));
    expect(await screen.findByTestId('public-port-card')).toBeInTheDocument();
    expect(screen.queryByTestId('app-network-card')).not.toBeInTheDocument();
  });
});
