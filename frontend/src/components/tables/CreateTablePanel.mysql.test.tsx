import { describe, test, expect, beforeEach, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { CreateTablePanel } from './CreateTablePanel';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { post: vi.fn(), put: vi.fn() } }));

function renderPanel(mysql: boolean) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <CreateTablePanel open onClose={vi.fn()} projectId="p1" canGrantRead mysql={mysql} schema={mysql ? 'shop' : 'public'} />
    </QueryClientProvider>,
  );
}

function offeredTypes(): string[] {
  return Array.from(document.querySelectorAll('#column-type-options option')).map((option) => option.getAttribute('value') ?? '');
}

// ADR 0041 decision 9: the table editor offers the project's engine's types.
describe('CreateTablePanel types', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    vi.mocked(api.put).mockResolvedValue({ data: {} } as never);
  });

  test('a MySQL table starts with an auto-increment id and offers MySQL types', async () => {
    renderPanel(true);
    expect(screen.getByDisplayValue('bigint unsigned auto_increment')).toBeInTheDocument();
    expect(offeredTypes()).toEqual(expect.arrayContaining(['tinyint(1)', 'datetime', 'json', "enum('a','b')"]));
    expect(offeredTypes()).not.toContain('timestamptz');

    fireEvent.change(screen.getByTestId('table-name-input'), { target: { value: 'orders' } });
    fireEvent.click(screen.getByText('Anyone can read (anon)'));
    fireEvent.click(screen.getByTestId('create-table-submit'));
    await waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(vi.mocked(api.post).mock.calls[0][1]).toMatchObject({
      name: 'orders', columns: [{ name: 'id', type: 'bigint unsigned auto_increment', primaryKey: true }],
    });
    await waitFor(() => expect(api.put).toHaveBeenCalled());
    expect(vi.mocked(api.put).mock.calls[0][0]).toContain('/permissions/tables/shop.orders/roles/anon/select');
  });

  test('a Postgres table keeps serial and the Postgres types', () => {
    renderPanel(false);
    expect(screen.getByDisplayValue('serial')).toBeInTheDocument();
    expect(offeredTypes()).toEqual(expect.arrayContaining(['timestamptz', 'jsonb', 'uuid']));
  });
});
