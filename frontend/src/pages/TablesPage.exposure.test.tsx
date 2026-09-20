import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TablesPage } from './TablesPage';
import { api } from '../api/client';
import type { TableGrant } from '../hooks/useTableGrants';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn(), put: vi.fn() },
}));

const TABLES = [
  { name: 'orders', schema: 'public', type: 'BASE TABLE' },
  { name: 'customers', schema: 'public', type: 'BASE TABLE' },
];

function renderPage(grants: TableGrant[]) {
  const state = { grants: [...grants], nextId: 1 };

  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url.endsWith('/table-grants/')) {
      return Promise.resolve({
        data: { projectId: 'p1', enforced: true, grants: state.grants },
      } as never);
    }
    if (url.endsWith('/tables')) return Promise.resolve({ data: TABLES } as never);
    return Promise.resolve({ data: [] } as never);
  });
  vi.mocked(api.post).mockImplementation((url: string, body: unknown) => {
    if (url.endsWith('/table-grants/')) {
      const g = body as Omit<TableGrant, 'id' | 'projectId'>;
      state.grants.push({ ...g, id: `g${state.nextId++}`, projectId: 'p1' });
      return Promise.resolve({ data: {} } as never);
    }
    return Promise.resolve({ data: {} } as never);
  });
  vi.mocked(api.delete).mockImplementation((url: string) => {
    const m = /table-grants\/(.+)$/.exec(url);
    if (m) state.grants = state.grants.filter((g) => g.id !== m[1]);
    return Promise.resolve({ data: {} } as never);
  });

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/projects/p1/tables']}>
        <Routes>
          <Route path="/projects/:projectId/tables" element={<TablesPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return state;
}

function grant(id: string, resource: string, role: string): TableGrant {
  return { id, projectId: 'p1', resource, operations: ['SELECT'], role, enabled: true };
}

beforeEach(() => vi.clearAllMocks());

describe('TablesPage API exposure', () => {
  // The bug this feature answers: an operator could not see which tables their
  // end users reach. Reachable and unreachable must differ at a glance, in the
  // list, without opening anything.
  test('marks each table in the list as reachable or not', async () => {
    renderPage([grant('g1', 'public.orders', 'anon')]);

    const orders = await screen.findByTestId('exposure-toggle-orders');
    const customers = await screen.findByTestId('exposure-toggle-customers');

    await waitFor(() => expect(orders).toHaveAttribute('aria-pressed', 'true'));
    expect(customers).toHaveAttribute('aria-pressed', 'false');
    expect(orders).toHaveAccessibleName(/reachable through the API/i);
  });

  test('one click makes an unreachable table reachable', async () => {
    renderPage([]);
    const user = userEvent.setup();

    const customers = await screen.findByTestId('exposure-toggle-customers');
    expect(customers).toHaveAttribute('aria-pressed', 'false');

    await user.click(customers);

    await waitFor(() =>
      expect(screen.getByTestId('exposure-toggle-customers')).toHaveAttribute('aria-pressed', 'true'),
    );
    expect(api.post).toHaveBeenCalledWith(
      '/provision/p1/table-grants/',
      expect.objectContaining({ resource: 'public.customers', role: 'anon' }),
    );
    expect(api.post).toHaveBeenCalledWith(
      '/provision/p1/table-grants/',
      expect.objectContaining({ resource: 'public.customers', role: 'authenticated' }),
    );
  });

  test('clicking a reachable table takes it back out of the API', async () => {
    renderPage([grant('g1', 'public.orders', 'anon'), grant('g2', 'public.orders', 'authenticated')]);
    const user = userEvent.setup();

    const orders = await screen.findByTestId('exposure-toggle-orders');
    await waitFor(() => expect(orders).toHaveAttribute('aria-pressed', 'true'));

    await user.click(orders);

    await waitFor(() =>
      expect(screen.getByTestId('exposure-toggle-orders')).toHaveAttribute('aria-pressed', 'false'),
    );
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/table-grants/g1');
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/table-grants/g2');
  });

  // Selecting a table and toggling its exposure are different intents; the
  // toggle must not drag the browser to another table under the operator.
  test('toggling exposure does not change the selected table', async () => {
    renderPage([]);
    const user = userEvent.setup();

    await user.click(await screen.findByTestId('table-item-orders'));
    await user.click(screen.getByTestId('exposure-toggle-customers'));

    await waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(screen.getByTestId('table-item-orders')).toHaveAttribute('aria-current', 'true');
  });
});
