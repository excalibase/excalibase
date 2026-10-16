import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SqlEditorPage } from './SqlEditorPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

function renderFor(projectId: string, databaseType: string) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === `/provision/${projectId}`) return Promise.resolve({ data: { projectId, databaseType, status: 'ACTIVE' } } as never);
    return Promise.resolve({ data: [{ name: 'orders', schema: 'shop', type: 'BASE TABLE', comment: null }] } as never);
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/project/${projectId}/sql`]}>
        <Routes>
          <Route path="/project/:projectId/sql" element={<SqlEditorPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// ADR 0041 decision 9: the editor speaks the project's engine.
describe('SqlEditorPage engine', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
  });

  test('a MySQL project is told that DDL is not rolled back', async () => {
    renderFor('p-my', 'MYSQL');
    expect(await screen.findByTestId('sql-mysql-ddl-note')).toHaveTextContent(
      /MySQL DDL is not transactional: a script that fails part-way is not rolled back/,
    );
    expect(screen.getByTestId('sql-dialect')).toHaveTextContent('MySQL');
  });

  test('a Postgres project gets no such note', async () => {
    renderFor('p-pg', 'POSTGRESQL');
    expect(await screen.findByTestId('sql-dialect')).toHaveTextContent('PostgreSQL');
    expect(screen.queryByTestId('sql-mysql-ddl-note')).not.toBeInTheDocument();
  });
});
