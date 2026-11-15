import { describe, test, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AdminRouteGuard } from './AdminRouteGuard';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn() } }));

function renderGuard(config: object, host: string) {
  vi.spyOn(globalThis, 'location', 'get').mockReturnValue({ hostname: host } as Location);
  vi.mocked(api.get).mockResolvedValue({ data: config } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/admin']}>
        <Routes>
          <Route path="/admin" element={<AdminRouteGuard><div data-testid="console" /></AdminRouteGuard>} />
          <Route path="/" element={<div data-testid="home" />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('AdminRouteGuard', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => vi.restoreAllMocks());

  test('the console shows on the admin host', async () => {
    renderGuard({ deploymentMode: 'cloud', adminHost: 'admin.excalibase.io' }, 'admin.excalibase.io');
    expect(await screen.findByTestId('console')).toBeInTheDocument();
  });

  test('any other host is sent to the front page when an admin host is set', async () => {
    renderGuard({ deploymentMode: 'cloud', adminHost: 'admin.excalibase.io' }, 'app.excalibase.io');
    expect(await screen.findByTestId('home')).toBeInTheDocument();
    expect(screen.queryByTestId('console')).not.toBeInTheDocument();
  });

  test('with no admin host configured the console shows wherever it is served', async () => {
    renderGuard({ deploymentMode: 'selfhosted' }, 'localhost');
    expect(await screen.findByTestId('console')).toBeInTheDocument();
  });
});
