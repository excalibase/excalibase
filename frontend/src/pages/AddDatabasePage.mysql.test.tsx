import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AddDatabasePage } from './AddDatabasePage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => vi.fn() };
});

function stub(mysql: boolean) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    const answers: Record<string, unknown> = {
      '/provision/proj-1': { projectId: 'proj-1', status: 'ACTIVE', noDatabase: true, canAddDatabase: true },
      '/postgres/catalog': { majors: [{ major: '17', available: true, documentDb: true }] },
      '/mysql/catalog': { defaultVersion: '8.4.10', versions: [{ version: '8.4.10' }, { version: '8.4.9' }] },
      '/config': { deploymentMode: 'cloud', features: { mysql } },
    };
    return url in answers ? Promise.resolve({ data: answers[url] } as never) : Promise.reject(new Error(url));
  });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/database/add']}>
        <Routes>
          <Route path="/project/:projectId/database/add" element={<AddDatabasePage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// ADR 0041 decision 9: the version picker follows the engine, MySQL behind its flag.
describe('AddDatabasePage MySQL', () => {
  beforeEach(() => vi.clearAllMocks());

  test('adds a MySQL database on the chosen catalogue version', async () => {
    stub(true);
    vi.mocked(api.post).mockResolvedValue({ data: { projectId: 'proj-1', status: 'PROVISIONING', noDatabase: true } } as never);
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByTestId('add-engine-MYSQL'));
    expect(await screen.findByTestId('mysql-version-8.4.10')).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByTestId('pg-version-section')).not.toBeInTheDocument();
    await user.click(screen.getByTestId('mysql-version-8.4.9'));
    await user.click(screen.getByTestId('add-database-submit'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith(
      '/provision/proj-1/database', { databaseType: 'MYSQL', mysqlVersion: '8.4.9' }, expect.anything(),
    ));
  });

  test('offers no MySQL while the feature is off', async () => {
    stub(false);
    renderPage();
    expect(await screen.findByTestId('add-engine-POSTGRESQL')).toBeInTheDocument();
    expect(screen.queryByTestId('add-engine-MYSQL')).not.toBeInTheDocument();
  });
});
