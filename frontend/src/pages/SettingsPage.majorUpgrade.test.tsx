import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SettingsPage } from './SettingsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

function renderSettings(majorupgrade: boolean) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') {
      return Promise.resolve({ data: { deploymentMode: 'cloud', features: { majorupgrade } } } as never);
    }
    if (url === '/provision/p-1') {
      return Promise.resolve({
        data: { projectId: 'p-1', orgId: 'o-1', databaseType: 'POSTGRESQL', tier: 'FREE', status: 'ACTIVE', postgresVersion: '16' },
      } as never);
    }
    if (url === '/provision/p-1/major-upgrade') {
      return Promise.resolve({
        data: {
          engine: 'postgresql', deploymentMode: 'k8s', currentVersion: '16', targets: ['17'], supported: true,
          reason: '', blockers: [], downtime: 'A few minutes offline.', latest: null,
        },
      } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p-1/settings']}>
        <Routes>
          <Route path="/project/:projectId/settings" element={<SettingsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('SettingsPage — major upgrade', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows the card after the minor upgrade card when the flag is on', async () => {
    renderSettings(true);
    const card = await screen.findByTestId('major-upgrade-card');
    const minor = screen.getByTestId('minor-upgrade-card');
    expect(minor.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  test('shows nothing and calls nothing when the flag is off', async () => {
    renderSettings(false);
    expect(await screen.findByTestId('minor-upgrade-card')).toBeInTheDocument();
    expect(screen.queryByTestId('major-upgrade-card')).not.toBeInTheDocument();
    expect(vi.mocked(api.get).mock.calls.some(([url]) => url === '/provision/p-1/major-upgrade')).toBe(false);
  });
});
