import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SettingsPage } from './SettingsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

function renderSettings(extra: Record<string, unknown> = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p-1') {
      return Promise.resolve({
        data: { projectId: 'p-1', orgId: 'o-1', databaseType: '', tier: 'FREE', status: 'ACTIVE', noDatabase: true, deletionProtection: false, ...extra },
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

// EXC-426: settings of a project created without a database show only what
// the project has; every database card would read a database that is not there.
describe('SettingsPage — no database', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  test('shows no database settings and keeps the project controls', async () => {
    renderSettings();
    expect(await screen.findByTestId('settings-no-database')).toHaveTextContent('no database');
    for (const id of ['connect-section', 'lifecycle-section', 'public-port-card', 'cluster-settings-card']) {
      expect(screen.queryByTestId(id)).not.toBeInTheDocument();
    }
    expect(screen.getByTestId('delete-project-btn')).toBeEnabled();
    expect(screen.getByTestId('deletion-grace-note')).not.toHaveTextContent('stops this project');
    expect(api.get).not.toHaveBeenCalledWith('/projects/p-1/db-endpoint');
  });
});
