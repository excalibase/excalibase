import { describe, test, expect, beforeEach, afterEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TablesPage } from './TablesPage';
import { api } from '../api/client';

// The exposure half of this page lives in TablesPage.exposure.test.tsx. This
// file covers the rest of what an operator does here: browse a table, read
// and edit its rows, page and sort them, and change its shape.

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn(), put: vi.fn() },
}));

const TABLES = [
  { name: 'orders', schema: 'public', type: 'BASE TABLE' },
  { name: 'customers', schema: 'public', type: 'BASE TABLE' },
];

const COLUMNS = [
  {
    name: 'id',
    dataType: 'integer',
    nullable: false,
    primaryKey: true,
    unique: true,
    defaultValue: "nextval('orders_id_seq'::regclass)",
    ordinalPosition: 1,
    comment: null,
    characterMaximumLength: null,
    numericPrecision: 32,
    numericScale: 0,
  },
  {
    name: 'note',
    dataType: 'text',
    nullable: true,
    primaryKey: false,
    unique: false,
    defaultValue: null,
    ordinalPosition: 2,
    comment: null,
    characterMaximumLength: null,
    numericPrecision: null,
    numericScale: null,
  },
];

const ROWS = {
  columns: [
    { name: 'id', dataType: 'integer' },
    { name: 'note', dataType: 'text' },
  ],
  rows: [
    [1, 'first order'],
    [2, null],
  ],
  totalCount: 120,
};

interface RowsCall {
  readonly limit?: number;
  readonly offset?: number;
  readonly sort?: string;
  readonly order?: string;
}

function renderPage() {
  const rowsCalls: RowsCall[] = [];

  vi.mocked(api.get).mockImplementation((url: string, cfg?: unknown) => {
    if (url.endsWith('/rows')) {
      rowsCalls.push(((cfg as { params?: RowsCall })?.params ?? {}) as RowsCall);
      return Promise.resolve({ data: ROWS } as never);
    }
    if (url.endsWith('/columns')) return Promise.resolve({ data: COLUMNS } as never);
    if (url.endsWith('/tables')) return Promise.resolve({ data: TABLES } as never);
    if (url.endsWith('/table-grants/')) {
      return Promise.resolve({ data: { projectId: 'p1', enforced: true, grants: [] } } as never);
    }
    return Promise.resolve({ data: [] } as never);
  });
  vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
  vi.mocked(api.patch).mockResolvedValue({ data: {} } as never);
  vi.mocked(api.delete).mockResolvedValue({ data: {} } as never);

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
  return { rowsCalls };
}

/** Picks `orders` in the sidebar and waits for its rows to arrive. */
async function selectOrders(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByTestId('table-item-orders'));
  await screen.findByText('first order');
}

beforeEach(() => vi.clearAllMocks());

describe('TablesPage data browsing', () => {
  test('shows an empty state until a table is picked', async () => {
    renderPage();
    expect(await screen.findByText(/select a table from the sidebar/i)).toBeInTheDocument();
  });

  test('selecting a table shows its rows and total count', async () => {
    renderPage();
    const user = userEvent.setup();
    await selectOrders(user);

    expect(screen.getByText('(120 rows)')).toBeInTheDocument();
    expect(screen.getByText('first order')).toBeInTheDocument();
    // A null cell must read as NULL, not as an empty box.
    expect(screen.getByText('NULL')).toBeInTheDocument();
  });

  // Row data goes stale the moment anything else writes to the table; the
  // operator needs a way to re-read it that is not a page reload.
  test('Refresh re-reads the rows from the database', async () => {
    const { rowsCalls } = renderPage();
    const user = userEvent.setup();
    await selectOrders(user);
    const before = rowsCalls.length;

    await user.click(screen.getByTestId('refresh-rows-btn'));

    await waitFor(() => expect(rowsCalls.length).toBeGreaterThan(before));
  });

  test('sorting by a column re-queries the server, not the visible page', async () => {
    const { rowsCalls } = renderPage();
    const user = userEvent.setup();
    await selectOrders(user);

    await user.click(screen.getByRole('button', { name: /^note/ }));

    await waitFor(() =>
      expect(rowsCalls.at(-1)).toMatchObject({ sort: 'note', order: 'asc', offset: 0 }),
    );

    await user.click(screen.getByRole('button', { name: /^note/ }));
    await waitFor(() => expect(rowsCalls.at(-1)).toMatchObject({ sort: 'note', order: 'desc' }));
  });

  test('paging forward asks for the next window of rows', async () => {
    const { rowsCalls } = renderPage();
    const user = userEvent.setup();
    await selectOrders(user);

    const pager = screen.getByText(/^Page 1 of/).parentElement as HTMLElement;
    const [, next] = within(pager).getAllByRole('button');
    await user.click(next);

    await waitFor(() => expect(rowsCalls.at(-1)?.offset).toBe(50));
    expect(screen.getByText(/^Page 2 of/)).toBeInTheDocument();
  });

  test('editing a cell writes the new value against the row primary key', async () => {
    renderPage();
    const user = userEvent.setup();
    await selectOrders(user);

    await user.dblClick(screen.getByText('first order'));
    const editor = screen.getByDisplayValue('first order');
    await user.clear(editor);
    await user.type(editor, 'amended{Enter}');

    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/schema/p1/tables/orders/rows', {
        pk: { column: 'id', value: '1' },
        data: { note: 'amended' },
      }),
    );
  });

  // Deleting a row is irreversible, so it must go through the confirm modal
  // rather than straight from the click.
  test('deleting a row confirms first, then deletes it', async () => {
    renderPage();
    const user = userEvent.setup();
    await selectOrders(user);

    const firstRow = screen.getByText('first order').closest('tr') as HTMLElement;
    await user.click(within(firstRow).getAllByRole('button').at(-1) as HTMLElement);

    expect(await screen.findByText(/delete row with id=1/i)).toBeInTheDocument();
    expect(api.delete).not.toHaveBeenCalled();

    await user.click(screen.getByTestId('modal-confirm'));

    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/schema/p1/tables/orders/rows', {
        data: { pk: { column: 'id', value: '1' } },
      }),
    );
  });

  test('inserting a row skips the serial primary key and posts the typed values', async () => {
    renderPage();
    const user = userEvent.setup();
    await selectOrders(user);

    await user.click(screen.getByTestId('insert-row-btn'));

    // id defaults from a sequence — offering it would invite a collision.
    expect(screen.queryByLabelText(/^id/)).not.toBeInTheDocument();
    await user.type(screen.getByLabelText(/^note/), 'a new order');
    await user.click(screen.getByTestId('insert-row-submit'));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/schema/p1/tables/orders/rows', {
        data: { note: 'a new order' },
      }),
    );
  });

  test('exports the visible rows as CSV, quoting values and blanking nulls', async () => {
    // Blob.text() is async, so capture the promise and assert on it after.
    const captured: Promise<string>[] = [];
    vi.stubGlobal('URL', {
      ...URL,
      createObjectURL: (blob: Blob) => {
        captured.push(blob.text());
        return 'blob:csv';
      },
    });
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, 'click')
      .mockImplementation(() => undefined);

    renderPage();
    const user = userEvent.setup();
    await selectOrders(user);
    await user.click(screen.getByTestId('export-csv-btn'));

    expect(click).toHaveBeenCalled();
    await expect(captured[0]).resolves.toBe('id,note\n"1","first order"\n"2",');
    click.mockRestore();
  });
});

describe('TablesPage schema editing', () => {
  test('the schema view lists the table columns', async () => {
    renderPage();
    const user = userEvent.setup();
    await selectOrders(user);

    await user.click(screen.getByRole('button', { name: 'Schema' }));

    expect(await screen.findByTestId('column-row-id')).toBeInTheDocument();
    expect(screen.getByTestId('column-row-note')).toBeInTheDocument();
  });

  test('adding a column posts its name, type and nullability', async () => {
    renderPage();
    const user = userEvent.setup();
    await selectOrders(user);

    await user.click(screen.getByRole('button', { name: 'Schema' }));
    await user.click(screen.getByTestId('add-column-btn'));

    await user.type(screen.getByTestId('column-name-input'), 'shipped_at');
    await user.selectOptions(screen.getByLabelText('Type'), 'timestamptz');
    await user.click(screen.getByRole('checkbox', { name: /nullable/i }));
    await user.click(screen.getByTestId('add-column-submit'));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/schema/p1/tables/orders/columns', {
        name: 'shipped_at',
        type: 'timestamptz',
        nullable: false,
      }),
    );
  });

  test('dropping a column confirms before it drops', async () => {
    renderPage();
    const user = userEvent.setup();
    await selectOrders(user);

    await user.click(screen.getByRole('button', { name: 'Schema' }));
    const noteRow = await screen.findByTestId('column-row-note');
    await user.click(within(noteRow).getAllByRole('button').at(-1) as HTMLElement);

    expect(await screen.findByText(/remove column "note"/i)).toBeInTheDocument();
    await user.click(screen.getByTestId('modal-confirm'));

    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/schema/p1/tables/orders/columns/note'),
    );
  });

  // Dropping a table takes the data with it, so the modal demands the name be
  // typed out — a mis-click must not be enough.
  test('dropping a table requires typing its name and then clears the selection', async () => {
    renderPage();
    const user = userEvent.setup();
    await selectOrders(user);

    await user.click(screen.getByTestId('drop-table-btn'));
    expect(screen.getByTestId('modal-confirm')).toBeDisabled();

    await user.type(screen.getByTestId('confirm-input'), 'orders');
    await user.click(screen.getByTestId('modal-confirm'));

    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/schema/p1/tables/orders', {
        params: { cascade: true },
      }),
    );
    expect(await screen.findByText(/select a table from the sidebar/i)).toBeInTheDocument();
  });

  test('the create-table panel opens from the sidebar', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await screen.findByTestId('new-table-btn'));

    expect(await screen.findByTestId('table-name-input')).toBeInTheDocument();
  });
});

afterEach(() => vi.unstubAllGlobals());
