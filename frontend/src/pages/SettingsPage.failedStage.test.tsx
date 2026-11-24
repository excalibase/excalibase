import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SettingsPage } from './SettingsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

// EXC-850: the failed-provisioning banner names the step in product words.
describe('SettingsPage — failed provisioning banner', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockImplementation((url: string) =>
      url === '/provision/p-1'
        ? Promise.resolve({
            data: {
              projectId: 'p-1', orgId: 'o-1', databaseType: 'POSTGRESQL', tier: 'FREE', status: 'FAILED',
              currentStage: 'FAILED', failureStage: 'NAMESPACE_CREATION', failureStep: 'create namespace',
              deletionProtection: false,
            },
          } as never)
        : Promise.reject(new Error(`unexpected GET ${url}`)),
    );
  });

  test('shows the friendly stage label, never the raw stage or step', async () => {
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter initialEntries={['/project/p-1/settings']}>
          <Routes>
            <Route path="/project/:projectId/settings" element={<SettingsPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const banner = await screen.findByText(/Provisioning failed at/);
    expect(banner).toHaveTextContent('Provisioning failed at stage Environment');
    expect(banner.textContent).not.toMatch(/NAMESPACE|namespace/i);
  });
});
