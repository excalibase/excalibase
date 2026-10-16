import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { IndexesPage } from './IndexesPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

function respond(url: string) {
  if (url === '/provision/p-my') return { databaseType: 'MYSQL', databaseName: 'shop', status: 'ACTIVE' };
  if (url === '/schema/p-my/tables') return [{ name: 'orders', schema: 'shop', type: 'BASE TABLE' }];
  if (url === '/schema/p-my/tables/orders/indexes') return [{ name: 'orders_paid_idx', columns: ['paid'], unique: false, type: 'btree' }];
  return [{ name: 'paid', dataType: 'tinyint(1)' }];
}

// ADR 0041: MySQL indexes are B-trees and are named per table.
describe('IndexesPage on a MySQL project', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockImplementation((url: string) => Promise.resolve({ data: respond(url) } as never));
    vi.mocked(api.delete).mockResolvedValue({} as never);
  });

  test('offers B-tree only and drops an index on its table', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/project/p-my/database/indexes']}>
          <Routes>
            <Route path="/project/:projectId/database/indexes" element={<IndexesPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await screen.findByRole('option', { name: 'orders' });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/provision/p-my'));
    fireEvent.change(screen.getByTestId('table-selector'), { target: { value: 'orders' } });
    fireEvent.click(screen.getByTestId('create-index-btn'));
    const types = within(screen.getByLabelText('Type')).getAllByRole('option').map((option) => option.textContent);
    expect(types).toEqual(['btree']);

    fireEvent.click(await screen.findByRole('button', { name: 'Drop index orders_paid_idx' }));
    fireEvent.click(screen.getByRole('button', { name: 'Drop Index' }));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/schema/p-my/indexes/orders_paid_idx', {
      params: { schema: 'public', table: 'orders' },
    }));
  });
});
