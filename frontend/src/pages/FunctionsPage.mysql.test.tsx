import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { FunctionsPage } from './FunctionsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

// ADR 0041: tracked functions are not offered on MySQL, and the page says why.
describe('FunctionsPage on a MySQL project', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockImplementation((url: string) => {
      if (url === '/provision/p-my') return Promise.resolve({ data: { projectId: 'p-my', databaseType: 'MYSQL', status: 'ACTIVE' } } as never);
      return Promise.reject(new Error(`unexpected GET ${url}`));
    });
  });

  test('explains that MySQL functions return scalars and reads no Postgres catalog', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/project/p-my/database/functions']}>
          <Routes>
            <Route path="/project/:projectId/database/functions" element={<FunctionsPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    );
    const notice = await screen.findByTestId('mysql-functions-notice');
    expect(notice).toHaveTextContent(/MySQL functions return scalars/);
    expect(notice).toHaveTextContent(/permissions apply to rows/);
    expect(screen.queryByTestId('create-function-btn')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(expect.stringContaining('/schema/p-my/functions'), expect.anything());
  });
});
