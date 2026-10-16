import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TablesPage } from './TablesPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn(), put: vi.fn() },
}));

const COLUMNS = [
  { name: 'id', dataType: 'bigint unsigned', nullable: false, primaryKey: true, unique: true, defaultValue: null, ordinalPosition: 1 },
  { name: 'paid', dataType: 'tinyint(1)', nullable: false, primaryKey: false, unique: false, defaultValue: '0', ordinalPosition: 2 },
];

function respond(url: string) {
  if (url === '/provision/p-my') return { databaseType: 'MYSQL', databaseName: 'shop', status: 'ACTIVE' };
  if (url === '/schema/p-my/tables') return [{ name: 'orders', schema: 'shop', type: 'BASE TABLE' }];
  if (url.endsWith('/columns')) return COLUMNS;
  if (url.endsWith('/rows')) return { columns: [], rows: [], totalCount: 0 };
  return [];
}

// ADR 0041 decision 9: one table editor, reading the project's engine.
describe('TablesPage on a MySQL project', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockImplementation((url: string) => Promise.resolve({ data: respond(url) } as never));
  });

  test('adds columns with MySQL types and offers no import', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/project/p-my/database/tables']}>
          <Routes>
            <Route path="/project/:projectId/database/tables" element={<TablesPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await userEvent.click(await screen.findByTestId('table-item-orders'));
    expect(screen.queryByTestId('import-table-btn')).not.toBeInTheDocument();
    await userEvent.click(screen.getByText('Schema'));
    await userEvent.click(screen.getByTestId('add-column-btn'));
    const types = within(screen.getByLabelText('Type')).getAllByRole('option').map((option) => option.textContent);
    expect(types).toEqual(expect.arrayContaining(['tinyint(1)', 'datetime', 'json']));
    expect(types).not.toContain('timestamptz');
  });
});
